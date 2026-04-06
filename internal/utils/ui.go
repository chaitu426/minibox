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
	fmt.Printf(ColorGreen+"✔ "+format+ColorReset+"\n", a...)
}

func PrintInfo(format string, a ...interface{}) {
	if !colorEnabled() {
		fmt.Printf("..  "+format+"\n", a...)
		return
	}
	fmt.Printf(ColorCyan+"ℹ "+format+ColorReset+"\n", a...)
}

func PrintWarning(format string, a ...interface{}) {
	if !colorEnabled() {
		fmt.Printf("!!  "+format+"\n", a...)
		return
	}
	fmt.Printf(ColorYellow+"⚠ "+format+ColorReset+"\n", a...)
}

func PrintError(format string, a ...interface{}) {
	if !colorEnabled() {
		fmt.Printf("ERR "+format+"\n", a...)
		return
	}
	fmt.Printf(ColorRed+ColorBold+"✘ "+format+ColorReset+"\n", a...)
}

func Banner() {
	if !colorEnabled() {
		fmt.Println("minibox")
		fmt.Println("Low-level container engine for Linux")
		fmt.Println()
		return
	}
	fmt.Println(ColorCyan + ColorBold + `
  __  __ _       _      _____             _             
 |  \/  (_)     (_)    |  __ \           | |            
 | \  / |_ _ __  _ ____| |  | | ___   ___| | _____ _ __ 
 | |\/| | | '_ \| |____| |  | |/ _ \ / __| |/ / _ \ '__|
 | |  | | | | | | |    | |__| | (_) | (__|   <  __/ |   
 |_|  |_|_|_| |_|_|    |_____/ \___/ \___|_|\_\___|_|   
` + ColorReset)
	fmt.Println(ColorWhite + "       Low-Level Container Engine for Linux" + ColorReset)
	fmt.Println()
}

func colorEnabled() bool {
	return os.Getenv("NO_COLOR") == "" && os.Getenv("MINIBOX_PLAIN") == ""
}
