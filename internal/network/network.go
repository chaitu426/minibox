package network

import (
	"fmt"
	"net"
	"os"
	"runtime"
	"sync/atomic"

	"github.com/coreos/go-iptables/iptables"
	"github.com/vishvananda/netlink"
	"github.com/vishvananda/netns"

	"github.com/chaitu426/mini-docker/internal/security"
)

const (
	BridgeName = "mini-docker0"
	Subnet     = "172.19.0"
	BridgeIP   = "172.19.0.1/24"
	GatewayIP  = "172.19.0.1"
)

// ipCounter is used to generate unique container IPs starting from .2
var ipCounter uint32 = 1

// AllocateIP assigns the next available container IP in the 172.19.0.x range.
func AllocateIP() string {
	n := atomic.AddUint32(&ipCounter, 1)
	return fmt.Sprintf("%s.%d", Subnet, n)
}

// SetupBridge creates the mini-docker0 bridge and enables NAT.
func SetupBridge() error {
	// 1. Create Bridge if it doesn't exist
	la := netlink.NewLinkAttrs()
	la.Name = BridgeName
	br := &netlink.Bridge{LinkAttrs: la}

	if err := netlink.LinkAdd(br); err != nil && err.Error() != "file exists" {
		return fmt.Errorf("failed to create bridge %s: %v", BridgeName, err)
	}

	// 2. Assign IP to Bridge
	addr, _ := netlink.ParseAddr(BridgeIP)
	if err := netlink.AddrAdd(br, addr); err != nil && err.Error() != "file exists" {
		return fmt.Errorf("failed to add IP %s to bridge: %v", BridgeIP, err)
	}

	// 3. Bring Bridge Up
	if err := netlink.LinkSetUp(br); err != nil {
		return fmt.Errorf("failed to set bridge %s up: %v", BridgeName, err)
	}

	// 4. Enable IP forwarding and local routing safely
	enableFeatureIfOff("/proc/sys/net/ipv4/ip_forward")
	enableFeatureIfOff("/proc/sys/net/ipv4/conf/all/route_localnet")
	enableFeatureIfOff("/proc/sys/net/ipv4/conf/default/route_localnet")

	// 5. Add NAT Masquerade rules via coreos/go-iptables
	ipt, err := iptables.New()
	if err != nil {
		return fmt.Errorf("failed to init iptables: %v", err)
	}

	// Masquerade for internet access
	_ = ipt.AppendUnique("nat", "POSTROUTING", "-s", Subnet+".0/24", "!", "-o", BridgeName, "-j", "MASQUERADE")
	// Masquerade for bridge traffic (localhost DNAT support)
	_ = ipt.AppendUnique("nat", "POSTROUTING", "-d", Subnet+".0/24", "-j", "MASQUERADE")

	fmt.Printf("[network] Bridge %s ready with IP %s\n", BridgeName, BridgeIP)
	return nil
}

// TeardownBridge removes global bridge/NAT rules created by SetupBridge.
func TeardownBridge() error {
	ipt, _ := iptables.New()
	_ = ipt.Delete("nat", "POSTROUTING", "-s", Subnet+".0/24", "!", "-o", BridgeName, "-j", "MASQUERADE")
	_ = ipt.Delete("nat", "POSTROUTING", "-d", Subnet+".0/24", "-j", "MASQUERADE")

	if link, err := netlink.LinkByName(BridgeName); err == nil {
		if err := netlink.LinkSetDown(link); err == nil {
			_ = netlink.LinkDel(link)
		}
	}
	return nil
}

// SetupContainerNetwork wires a veth pair between host bridge and container netns using native netlink/netns.
func SetupContainerNetwork(pid int, containerID string, ip string, portMap map[string]string) error {
	if !security.ValidContainerID(containerID) {
		return fmt.Errorf("invalid container id")
	}
	for hostPort, containerPort := range portMap {
		if err := security.ValidHostPort(hostPort); err != nil {
			return fmt.Errorf("invalid host port %q: %w", hostPort, err)
		}
		if err := security.ValidHostPort(containerPort); err != nil {
			return fmt.Errorf("invalid container port %q: %w", containerPort, err)
		}
	}
	hostVethName := "veth-" + containerID[:8]
	peerVethName := "vetp-" + containerID[:8]

	// 1. Create Veth Pair
	la := netlink.NewLinkAttrs()
	la.Name = hostVethName
	veth := &netlink.Veth{
		LinkAttrs: la,
		PeerName:  peerVethName,
	}
	if err := netlink.LinkAdd(veth); err != nil {
		return fmt.Errorf("failed to create veth pair: %v", err)
	}

	// 2. Attach Host Veth to Bridge and bring it up
	br, _ := netlink.LinkByName(BridgeName)
	if err := netlink.LinkSetMaster(veth, br); err != nil {
		return fmt.Errorf("failed to attach %s to bridge: %v", hostVethName, err)
	}
	if err := netlink.LinkSetUp(veth); err != nil {
		return fmt.Errorf("failed to set %s up: %v", hostVethName, err)
	}

	// 3. Move Peer Veth into container namespace
	peer, _ := netlink.LinkByName(peerVethName)
	if err := netlink.LinkSetNsPid(peer, pid); err != nil {
		return fmt.Errorf("failed to move peer into netns of pid %d: %v", pid, err)
	}

	// 4. Configure Networking INSIDE the container namespace using netns
	// LOCK the OS thread so this goroutine doesn't move to another thread while switched
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()

	// 4. Configure Networking INSIDE the container namespace using netns
	// Create a dedicated goroutine that locks its OS thread to perform netns operations safely.
	errStr := ""
	done := make(chan struct{})
	go func() {
		runtime.LockOSThread()
		defer close(done)

		originalNs, err := netns.Get()
		if err != nil {
			errStr = fmt.Sprintf("failed to get host netns: %v", err)
			return
		}
		defer originalNs.Close()

		containerNs, err := netns.GetFromPid(pid)
		if err != nil {
			errStr = fmt.Sprintf("failed to get netns for pid %d: %v", pid, err)
			return
		}
		defer containerNs.Close()

		if err := netns.Set(containerNs); err != nil {
			errStr = fmt.Sprintf("failed to enter container netns: %v", err)
			return
		}
		defer netns.Set(originalNs) // ALWAYS return to the original namespace

		if err := configureContainerInterfaces(peerVethName, ip); err != nil {
			errStr = fmt.Sprintf("container interface config failed: %v", err)
		}
	}()

	<-done
	if errStr != "" {
		return fmt.Errorf("%s", errStr)
	}

	// 5. Set up port forwarding (DNAT) rules on Host
	ipt, _ := iptables.New()
	for hostPort, containerPort := range portMap {
		dest := ip + ":" + containerPort
		// PREROUTING (External)
		_ = ipt.AppendUnique("nat", "PREROUTING", "-p", "tcp", "--dport", hostPort, "-j", "DNAT", "--to-destination", dest)
		// OUTPUT (Localhost)
		_ = ipt.AppendUnique("nat", "OUTPUT", "-p", "tcp", "-o", "lo", "--dport", hostPort, "-j", "DNAT", "--to-destination", dest)
		// FORWARD (Allow)
		_ = ipt.AppendUnique("filter", "FORWARD", "-p", "tcp", "-d", ip, "--dport", containerPort, "-j", "ACCEPT")
		fmt.Printf("[network] Port mapping: localhost:%s → container:%s\n", hostPort, containerPort)
	}

	fmt.Printf("[network] Container %s network ready — IP: %s\n", containerID, ip)
	return nil
}

func configureContainerInterfaces(vethName string, ip string) error {
	// Set lo up
	lo, err := netlink.LinkByName("lo")
	if err != nil {
		return fmt.Errorf("failed to find lo: %v", err)
	}
	if err := netlink.LinkSetUp(lo); err != nil {
		return fmt.Errorf("failed to set lo up: %v", err)
	}

	// Rename veth to eth0
	peer, err := netlink.LinkByName(vethName)
	if err != nil {
		return fmt.Errorf("failed to find peer %s in netns: %v", vethName, err)
	}
	if err := netlink.LinkSetDown(peer); err != nil {
		return fmt.Errorf("failed to set %s down: %v", vethName, err)
	}
	if err := netlink.LinkSetName(peer, "eth0"); err != nil {
		return fmt.Errorf("failed to rename %s to eth0: %v", vethName, err)
	}
	if err := netlink.LinkSetUp(peer); err != nil {
		return fmt.Errorf("failed to set eth0 up: %v", err)
	}

	// Assign IP to eth0
	eth0, err := netlink.LinkByName("eth0")
	if err != nil {
		return fmt.Errorf("failed to find eth0 after rename: %v", err)
	}
	addr, err := netlink.ParseAddr(ip + "/24")
	if err != nil {
		return fmt.Errorf("failed to parse IP %s: %v", ip, err)
	}
	if err := netlink.AddrAdd(eth0, addr); err != nil {
		return fmt.Errorf("failed to add IP %s to eth0: %v", ip, err)
	}

	// Add default route
	gw := net.ParseIP(GatewayIP)
	route := &netlink.Route{
		Scope:     netlink.SCOPE_UNIVERSE,
		LinkIndex: eth0.Attrs().Index,
		Gw:        gw,
	}
	return netlink.RouteAdd(route)
}

// TeardownContainerNetwork removes host-side veth and DNAT rules.
func TeardownContainerNetwork(containerID string, portMap map[string]string, ip string) {
	if !security.ValidContainerID(containerID) {
		return
	}
	hostVethName := "veth-" + containerID[:8]
	if link, err := netlink.LinkByName(hostVethName); err == nil {
		netlink.LinkDel(link)
	}

	ipt, _ := iptables.New()
	for hostPort, containerPort := range portMap {
		dest := ip + ":" + containerPort
		_ = ipt.Delete("nat", "PREROUTING", "-p", "tcp", "--dport", hostPort, "-j", "DNAT", "--to-destination", dest)
		_ = ipt.Delete("nat", "OUTPUT", "-p", "tcp", "-o", "lo", "--dport", hostPort, "-j", "DNAT", "--to-destination", dest)
		_ = ipt.Delete("filter", "FORWARD", "-p", "tcp", "-d", ip, "--dport", containerPort, "-j", "ACCEPT")
	}
}

func linkExists(name string) bool {
	_, err := netlink.LinkByName(name)
	return err == nil
}

func enableFeatureIfOff(path string) {
	data, err := os.ReadFile(path)
	if err == nil && len(data) > 0 && data[0] == '1' {
		return // already enabled
	}
	_ = os.WriteFile(path, []byte("1"), 0644)
}
