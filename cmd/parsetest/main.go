package main

import (
	"fmt"
	"os"

	"github.com/Dicklesworthstone/ntm/internal/pipeline"
)

func main() {
	wf, err := pipeline.ParseFile(os.Args[1])
	if err != nil {
		fmt.Println("PARSE ERROR:", err)
		os.Exit(1)
	}
	fmt.Printf("Parsed: %s (%d steps)\n", wf.Name, len(wf.Steps))
}
