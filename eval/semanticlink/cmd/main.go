package main

import (
	"fmt"
	"os"

	semanticlinkeval "github.com/CodeZen-Lizhi/zhixu/eval/semanticlink"
)

func main() {
	dataset, err := semanticlinkeval.LoadV2()
	if err != nil {
		fmt.Fprintln(os.Stderr, "semantic link eval dataset invalid")
		os.Exit(1)
	}
	report, err := semanticlinkeval.Evaluate(dataset)
	if err != nil {
		fmt.Fprintln(os.Stderr, "semantic link eval quality gate failed")
		os.Exit(1)
	}
	encoded, err := semanticlinkeval.MarshalReport(report)
	if err != nil {
		fmt.Fprintln(os.Stderr, "semantic link eval report encoding failed")
		os.Exit(1)
	}
	fmt.Println(string(encoded))
}
