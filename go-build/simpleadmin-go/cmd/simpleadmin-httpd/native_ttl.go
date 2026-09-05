//go:build linux
// +build linux

package main

import (
	"fmt"
	"log"
	"os"
	"os/exec"
	"strconv"
	"strings"
)

const (
	iptablesCommand  = "iptables"
	ip6tablesCommand = "ip6tables"
)

type ttlRuleSpec struct {
	command string
	target  string
	setFlag string
}

var ttlRuleSpecs = []ttlRuleSpec{
	{command: iptablesCommand, target: "TTL", setFlag: "--ttl-set"},
	{command: ip6tablesCommand, target: "HL", setFlag: "--hl-set"},
}

func runTTLCommand(args []string) {
	if len(args) == 0 {
		fmt.Println("Usage: simpleadmin-httpd ttl {apply|off|status}")
		os.Exit(1)
	}

	switch args[0] {
	case "apply", "start", "restart":
		value, err := readTTLValue()
		if err != nil {
			fmt.Println(err)
			os.Exit(1)
		}
		printLines(applyNativeTTL(value, false))
	case "off", "stop":
		printLines(setNativeTTL(0))
	case "status":
		value, enabled := currentTTLValue()
		fmt.Printf("enabled=%t ttl=%d\n", enabled, value)
	default:
		fmt.Println("Usage: simpleadmin-httpd ttl {apply|off|status}")
		os.Exit(1)
	}
}

func printLines(lines []string) {
	for _, line := range lines {
		fmt.Println(line)
	}
}

func applySavedTTLAtStartup() {
	value, err := readTTLValue()
	if err != nil {
		if err := writeTTLValue(0); err != nil {
			log.Printf("初始化 TTL 配置失败: %v", err)
		}
		return
	}
	if value <= 0 {
		return
	}
	for _, line := range applyNativeTTL(value, false) {
		log.Printf("TTL: %s", line)
	}
}

func setNativeTTL(value int) []string {
	logs := applyNativeTTL(value, false)
	if err := writeTTLValue(value); err != nil {
		logs = append(logs, "failed to write ttlvalue: "+err.Error())
	} else {
		logs = append(logs, "TTL value saved")
	}
	return logs
}

func applyNativeTTL(value int, persistLog bool) []string {
	logs := []string{"Applying TTL rules with Go native handler"}
	logs = append(logs, removeNativeTTLRules()...)

	if value <= 0 {
		logs = append(logs, "TTL disabled")
		return logs
	}

	logs = append(logs, fmt.Sprintf("Enabling TTL with value: %d", value))
	for _, spec := range ttlRuleSpecs {
		args := []string{"-t", "mangle", "-I", "POSTROUTING", "-o", "rmnet+", "-j", spec.target, spec.setFlag, strconv.Itoa(value)}
		if out, err := exec.Command(spec.command, args...).CombinedOutput(); err != nil {
			logs = append(logs, fmt.Sprintf("%s add failed: %v: %s", spec.command, err, strings.TrimSpace(string(out))))
			continue
		}
		logs = append(logs, fmt.Sprintf("%s rule added", spec.command))
	}

	if persistLog {
		logs = append(logs, "TTL value saved")
	}
	return logs
}

func removeNativeTTLRules() []string {
	var logs []string
	for _, spec := range ttlRuleSpecs {
		out, err := exec.Command(spec.command, "-t", "mangle", "-S", "POSTROUTING").CombinedOutput()
		if err != nil {
			logs = append(logs, fmt.Sprintf("%s list failed: %v: %s", spec.command, err, strings.TrimSpace(string(out))))
			continue
		}

		removed := 0
		for _, line := range strings.Split(string(out), "\n") {
			deleteArgs, ok := managedTTLDeleteArgs(line, spec.target, spec.setFlag)
			if !ok {
				continue
			}
			cmdArgs := append([]string{"-t", "mangle"}, deleteArgs...)
			if delOut, err := exec.Command(spec.command, cmdArgs...).CombinedOutput(); err != nil {
				logs = append(logs, fmt.Sprintf("%s delete failed: %v: %s", spec.command, err, strings.TrimSpace(string(delOut))))
				continue
			}
			removed++
		}
		logs = append(logs, fmt.Sprintf("%s removed %d managed TTL rule(s)", spec.command, removed))
	}
	return logs
}

func managedTTLDeleteArgs(ruleLine, target, setFlag string) ([]string, bool) {
	fields := strings.Fields(ruleLine)
	if len(fields) < 8 || fields[0] != "-A" || fields[1] != "POSTROUTING" {
		return nil, false
	}
	if !hasTokenPair(fields, "-o", "rmnet+") || !hasTokenPair(fields, "-j", target) || !hasToken(fields, setFlag) {
		return nil, false
	}
	args := append([]string{"-D", "POSTROUTING"}, fields[2:]...)
	return args, true
}

func hasTokenPair(fields []string, key, value string) bool {
	for i := 0; i+1 < len(fields); i++ {
		if fields[i] == key && fields[i+1] == value {
			return true
		}
	}
	return false
}

func hasToken(fields []string, token string) bool {
	for _, field := range fields {
		if field == token {
			return true
		}
	}
	return false
}

func readTTLValue() (int, error) {
	for _, path := range []string{runtimeTTLValueFile, legacyTTLValueFile} {
		data, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		value, err := strconv.Atoi(stripNonDigits(string(data)))
		if err != nil || value < 0 || value > 255 {
			return 0, fmt.Errorf("invalid ttlvalue in %s", path)
		}
		return value, nil
	}
	return 0, os.ErrNotExist
}

func writeTTLValue(value int) error {
	if value < 0 || value > 255 {
		return fmt.Errorf("invalid TTL value: %d", value)
	}
	return writePersistentFile(runtimeTTLValueFile, []byte(strconv.Itoa(value)+"\n"), 0644)
}
