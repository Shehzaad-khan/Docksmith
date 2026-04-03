package main

import (
	"fmt"
	"os"
)

func main() {
	if len(os.Args) < 2 {
		fmt.Println("Usage: docksmith <build|run|images|rmi>")
		os.Exit(1)
	}
	fmt.Println("docksmith: command not yet implemented:", os.Args[1])
}