package main

import "fmt"

var version = "dev"

func banner(value string) string {
	return fmt.Sprintf("AI-GDM %s", value)
}

func main() {
	fmt.Println(banner(version))
}
