// kube-dump: takes a JVM thread dump and/or heap dump (through jattach, so nothing is
// written to the container stdout) of the running pods of a Kubernetes service, saves
// them to files and prints a summary.
package main

import (
	"flag"
	"fmt"
	"os"

	"kube-dump/internal/collect"
	"kube-dump/internal/config"
	"kube-dump/internal/kube"
	"kube-dump/internal/ui"
)

// version is set at build time: -ldflags "-X main.version=...".
var version = "dev"

func main() {
	cfgPath := flag.String("config", "configuration.yml", "path to configuration file")
	envName := flag.String("env", "", "environment: kubeconfig context name; overrides env in config")
	service := flag.String("service", "", "service name; overrides service in config")
	module := flag.String("module", "", "module name: choose interactively among services <module>-*")
	modeFlag := flag.String("mode", "", "what to collect when a service is set: thread, heap or both; overrides mode in config")
	podFlag := flag.String("pod", "", "only this pod; * = every pod (needed for heap dumps of several pods); overrides pod in config")
	analyze := flag.String("analyze", "", "summarize an existing heap dump (.hprof) into a class histogram and exit; needs no cluster or config")
	yes := flag.Bool("yes", false, "do not ask before a heap dump; overrides heap_confirm in config")
	showVersion := flag.Bool("version", false, "print the version and exit")
	flag.Parse()

	if *showVersion {
		fmt.Println("kube-dump", version)
		return
	}
	if *analyze != "" {
		if err := collect.SummarizeHeapFile(*analyze, 30, true); err != nil {
			fatal(err)
		}
		return
	}
	collect.HandleInterrupt()

	cfg, err := config.Load(*cfgPath, *envName)
	if err != nil {
		fatal(err)
	}
	set := map[string]bool{}
	flag.Visit(func(f *flag.Flag) { set[f.Name] = true })
	if set["mode"] {
		if cfg.Mode, err = config.ParseMode(*modeFlag); err != nil {
			fatal(err)
		}
	}
	if set["pod"] {
		cfg.Pod = *podFlag
	}
	if *yes {
		cfg.HeapConfirm = false
	}
	switch {
	case *service != "":
		cfg.Service, cfg.Module = *service, ""
	case *module != "":
		cfg.Service, cfg.Module = "", *module
	}
	if cfg.Env, err = kube.ResolveEnv(cfg.Env); err != nil {
		fatal(err)
	}

	kc := &kube.Client{Context: cfg.Env, Namespace: cfg.Namespace, Timeout: cfg.KubectlTimeout}
	c := &collect.Collector{Cfg: cfg, Kube: kc, Confirm: ui.Confirm}

	// Explicit service: one run and exit.
	if cfg.Service != "" {
		opts := collect.Options{Mode: cfg.Mode, Pod: cfg.Pod, Yes: !cfg.HeapConfirm}
		if c.Services([]string{cfg.Service}, opts) > 0 {
			os.Exit(1)
		}
		return
	}

	// Interactive: after each run return to the service list until the user exits.
	all, err := kc.ListServices()
	if err != nil {
		fatal(err)
	}
	mod := cfg.Module
	for {
		if mod == "" {
			var ok bool
			if mod, ok = ui.AskModule(); !ok {
				return
			}
		}
		matched := kube.MatchServices(all, mod)
		if len(matched) == 0 {
			ui.NoServices(mod, kc)
			mod = ""
			continue
		}

		services, action, err := ui.ChooseServices(matched)
		if err != nil {
			fatal(err)
		}
		switch action {
		case ui.ActionExit:
			return
		case ui.ActionChangeModule:
			mod = ""
			continue
		case ui.ActionInterrupted:
			// Ctrl+C in the form: stay in the loop, back to the service list.
			fmt.Println("(interrupted — back to service list)")
			continue
		}
		mode, ok := ui.ChooseMode()
		if !ok {
			continue // back to the service list
		}
		opts := collect.Options{Mode: mode, Yes: !cfg.HeapConfirm}
		if mode.WantsHeap() {
			if len(services) != 1 {
				fmt.Println("A heap dump is taken for one service at a time: pick a single service.")
				continue
			}
			pods, err := kc.FindPods(services[0])
			if err == nil && len(pods) == 0 {
				err = fmt.Errorf("no running pods")
			}
			if err != nil {
				fmt.Fprintf(os.Stderr, "[%s] ERROR: %v\n", services[0], err)
				continue
			}
			if opts.Pod, ok = ui.ChoosePod(pods); !ok {
				continue
			}
		}
		c.Services(services, opts)
		fmt.Println("────────────────────────────────────────")
	}
}

func fatal(err error) {
	fmt.Fprintln(os.Stderr, "error:", err)
	os.Exit(1)
}
