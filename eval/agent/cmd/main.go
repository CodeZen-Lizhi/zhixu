package main

import (
	"fmt"
	"os"

	agenteval "github.com/CodeZen-Lizhi/zhixu/eval/agent"
)

func main() {
	dataset, err := agenteval.LoadV1()
	if err != nil {
		fmt.Fprintln(os.Stderr, "agent eval dataset invalid")
		os.Exit(1)
	}
	report, err := agenteval.Evaluate(dataset)
	if err != nil {
		fmt.Fprintln(os.Stderr, "agent eval quality gate failed")
		os.Exit(1)
	}
	encoded, err := agenteval.MarshalReport(report)
	if err != nil {
		fmt.Fprintln(os.Stderr, "agent eval report encoding failed")
		os.Exit(1)
	}
	fmt.Println(string(encoded))
}
