package network

import (
	"fmt"
	"io"
	"net"
	"os"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/coreos/go-iptables/iptables"
	"github.com/vishvananda/netlink"
	"github.com/vishvananda/netns"

	"github.com/chaitu426/minibox/internal/security"
)

var (
	proxyMu       sync.Mutex
	activeProxies = make(map[string][]io.Closer)
)

const (
	BridgeName = "minibox0"
	Subnet     = "172.19.0"
	BridgeIP   = "172.19.0.1/24"
	GatewayIP  = "172.19.0.1"
)

// Assign unique container IP (starting from .2).
var ipCounter uint32 = 1

// Allocate next available IP in 172.19.0.x.
func AllocateIP() string {
	n := atomic.AddUint32(&ipCounter, 1)
	return fmt.Sprintf("%s.%d", Subnet, n)
}

// miniboxBridgeDNATPrefix matches DNAT targets on the minibox bridge (e.g. 172.19.0.2:6379).
var miniboxBridgeDNATPrefix = Subnet + "."

// Delete stale DNAT rules to avoid localhost port mapping issues.
func removeStaleMiniboxNATForHostPort(ipt *iptables.IPTables, hostPort string) {
	for _, chain := range []string{"PREROUTING", "OUTPUT"} {
		for {
			rules, err := ipt.List("nat", chain)
			if err != nil {
				fmt.Printf("[warn] list nat/%s for port cleanup: %v\n", chain, err)
				break
			}
			removed := false
			prefix := "-A " + chain + " "
			for _, line := range rules {
				if !strings.HasPrefix(line, prefix) {
					continue
				}
				if chain == "OUTPUT" && !ruleHasOutInterfaceLo(line) {
					continue
				}
				if !ruleIsMiniboxTCPDNATToBridge(line, hostPort) {
					continue
				}
				spec := strings.Fields(strings.TrimPrefix(line, prefix))
				if err := ipt.Delete("nat", chain, spec...); err != nil {
					fmt.Printf("[warn] delete stale nat/%s dnat port=%s: %v\n", chain, hostPort, err)
				}
				removed = true
				break
			}
			if !removed {
				break
			}
		}
	}
}

func ruleHasOutInterfaceLo(line string) bool {
	fields := strings.Fields(line)
	for i := 0; i < len(fields)-1; i++ {
		if fields[i] == "-o" && fields[i+1] == "lo" {
			return true
		}
	}
	return false
}

func ruleIsMiniboxTCPDNATToBridge(line, hostPort string) bool {
	if !strings.Contains(line, "-j") || !strings.Contains(line, "DNAT") {
		return false
	}
	fields := strings.Fields(line)
	tcp := false
	for i := 0; i < len(fields)-1; i++ {
		if fields[i] == "-p" && fields[i+1] == "tcp" {
			tcp = true
			break
		}
	}
	if !tcp {
		return false
	}
	portOK := false
	for i := 0; i < len(fields)-1; i++ {
		if fields[i] == "--dport" && fields[i+1] == hostPort {
			portOK = true
			break
		}
	}
	if !portOK {
		return false
	}
	for i := 0; i < len(fields)-1; i++ {
		if fields[i] == "--to-destination" {
			dest := fields[i+1]
			if strings.HasPrefix(dest, miniboxBridgeDNATPrefix) {
				return true
			}
		}
	}
	return false
}

// SetupBridge creates the minibox0 bridge and enables NAT.
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

	// Ensure bridge exists. This makes daemon startup faster because the daemon can
	// bring up the bridge lazily on first container run.
	if !linkExists(BridgeName) {
		if err := SetupBridge(); err != nil {
			return fmt.Errorf("bridge not ready: %w", err)
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
	var proxies []io.Closer
	for hostPort, containerPort := range portMap {
		removeStaleMiniboxNATForHostPort(ipt, hostPort)
		dest := ip + ":" + containerPort
		// PREROUTING (External)
		_ = ipt.AppendUnique("nat", "PREROUTING", "-p", "tcp", "--dport", hostPort, "-j", "DNAT", "--to-destination", dest)
		// OUTPUT (Localhost)
		_ = ipt.AppendUnique("nat", "OUTPUT", "-p", "tcp", "-o", "lo", "--dport", hostPort, "-j", "DNAT", "--to-destination", dest)
		// FORWARD (Allow)
		_ = ipt.AppendUnique("filter", "FORWARD", "-p", "tcp", "-d", ip, "--dport", containerPort, "-j", "ACCEPT")

		if proxy, err := startTCPProxy(hostPort, ip, containerPort); err == nil {
			proxies = append(proxies, proxy)
		} else {
			fmt.Printf("[warn] network proxy host_port=%s err=%v\n", hostPort, err)
		}

		fmt.Printf("[network] Port mapping: localhost:%s → container:%s\n", hostPort, containerPort)
	}

	proxyMu.Lock()
	activeProxies[containerID] = proxies
	proxyMu.Unlock()

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

	proxyMu.Lock()
	if proxies, ok := activeProxies[containerID]; ok {
		for _, p := range proxies {
			p.Close()
		}
		delete(activeProxies, containerID)
	}
	proxyMu.Unlock()
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

func startTCPProxy(hostPort, targetIP, targetPort string) (io.Closer, error) {
	listener, err := net.Listen("tcp", "0.0.0.0:"+hostPort)
	if err != nil {
		return nil, err
	}
	go func() {
		for {
			client, err := listener.Accept()
			if err != nil {
				return
			}
			go func(c net.Conn) {
				defer c.Close()
				backend, err := net.Dial("tcp", targetIP+":"+targetPort)
				if err != nil {
					return
				}
				defer backend.Close()

				// Copy data bidirectionally
				go io.Copy(backend, c)
				io.Copy(c, backend)
			}(client)
		}
	}()
	return listener, nil
}
