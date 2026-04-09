package utils

import (
	"fmt"
	"os"
)

const (
	ColorReset  = "\033[0m"
	ColorRed    = "\033[31m"
	ColorGreen  = "\033[32m"
	ColorYellow = "\033[33m"
	ColorBlue   = "\033[34m"
	ColorPurple = "\033[35m"
	ColorCyan   = "\033[36m"
	ColorWhite  = "\033[37m"
	ColorBold   = "\033[1m"
)

func SprintfColor(color, format string, a ...interface{}) string {
	if !colorEnabled() {
		return fmt.Sprintf(format, a...)
	}
	return color + fmt.Sprintf(format, a...) + ColorReset
}

func PrintSuccess(format string, a ...interface{}) {
	if !colorEnabled() {
		fmt.Printf("OK  "+format+"\n", a...)
		return
	}
	fmt.Printf(ColorGreen+"OK  "+format+ColorReset+"\n", a...)
}

func PrintInfo(format string, a ...interface{}) {
	if !colorEnabled() {
		fmt.Printf("..  "+format+"\n", a...)
		return
	}
	fmt.Printf(ColorCyan+"..  "+format+ColorReset+"\n", a...)
}

func PrintWarning(format string, a ...interface{}) {
	if !colorEnabled() {
		fmt.Printf("!!  "+format+"\n", a...)
		return
	}
	fmt.Printf(ColorYellow+"!!  "+format+ColorReset+"\n", a...)
}

func PrintError(format string, a ...interface{}) {
	if !colorEnabled() {
		fmt.Printf("ERR "+format+"\n", a...)
		return
	}
	fmt.Printf(ColorRed+ColorBold+"ERR "+format+ColorReset+"\n", a...)
}

func Banner() {
	lines := []string{
		"minibox  —  minimal container engine (linux)",
		"https://github.com/chaitu426/minibox",
		"",
	}

	if !colorEnabled() {
		for _, l := range lines {
			fmt.Println(l)
		}
		return
	}

	fmt.Print(ColorCyan + ColorBold)
	for _, l := range lines {
		fmt.Println(l)
	}
	fmt.Print(ColorReset)
}

func colorEnabled() bool {
	return os.Getenv("NO_COLOR") == "" && os.Getenv("MINIBOX_PLAIN") == ""
}
