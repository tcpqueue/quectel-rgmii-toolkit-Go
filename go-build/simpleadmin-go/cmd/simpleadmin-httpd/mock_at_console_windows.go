//go:build windows
// +build windows

package main

import (
	"bufio"
	"fmt"
	"log"
	"os"
	"strings"
)

func startMockATConsole(enabled bool) {
	if !enabled {
		return
	}
	go runMockATConsoleLoop()
}

func runMockATConsoleLoop() {
	scanner := bufio.NewScanner(os.Stdin)
	scanner.Buffer(make([]byte, 1024), 1024*1024)
	printMockATConsoleHelp()
	for {
		fmt.Print("simpleadmin-mock> ")
		if !scanner.Scan() {
			if err := scanner.Err(); err != nil {
				log.Printf("Windows mock AT console stopped: %v", err)
			}
			return
		}
		cmd := strings.TrimSpace(scanner.Text())
		if cmd == "" {
			continue
		}
		fields := strings.Fields(cmd)
		switch strings.ToLower(fields[0]) {
		case "help", "?":
			printMockATConsoleHelp()
		case "at", "dashboard", "full", "at-test":
			readAndSetMockATPayload(scanner, mockATPayloadKindDashboard)
		case "qca", "qcainfo", "qca-test":
			readAndSetMockATPayload(scanner, mockATPayloadKindQCAInfo)
		case "qeng", "qeng-test":
			readAndSetMockATPayload(scanner, mockATPayloadKindQENG)
		case "clear":
			kind := ""
			if len(fields) > 1 {
				kind = fields[1]
			}
			if clearMockATPayload(kind) {
				fmt.Println("已清除 mock AT 输入：" + mockATPayloadStatusText())
			} else {
				fmt.Println("未知类型，可用：at / qca / qeng / clear")
			}
		case "show", "status":
			fmt.Println(mockATPayloadStatusText())
		case "parse":
			fmt.Println(currentMockDashboardParseJSON())
		default:
			fmt.Println("未知命令。输入 help 查看用法。")
		}
	}
}

func readAndSetMockATPayload(scanner *bufio.Scanner, kind string) {
	fmt.Printf("请输入 %s 测试数据，单独一行 .end 结束，单独一行 .cancel 取消。\n", kind)
	var lines []string
	for scanner.Scan() {
		line := scanner.Text()
		switch strings.TrimSpace(line) {
		case ".end":
			payload := strings.Join(lines, "\n")
			if !setMockATPayload(kind, payload) {
				fmt.Println("保存失败：未知类型")
				return
			}
			fmt.Println("已保存，首页下一次刷新会使用新的 mock AT 数据。")
			fmt.Println(mockATPayloadStatusText())
			fmt.Println("可输入 parse 在控制台查看后端解析结果。")
			return
		case ".cancel":
			fmt.Println("已取消。")
			return
		default:
			lines = append(lines, line)
		}
	}
	if err := scanner.Err(); err != nil {
		log.Printf("Windows mock AT console input failed: %v", err)
	}
}

func printMockATConsoleHelp() {
	fmt.Println("==============================================================")
	fmt.Println("Windows mock AT 手动输入")
	fmt.Println("  at      粘贴整段首页 AT 返回，写入 Windows mock 后端缓存")
	fmt.Println("  qca     粘贴 QCAINFO 返回，写入 Windows mock 后端缓存")
	fmt.Println("  qeng    粘贴 QENG 返回，写入 Windows mock 后端缓存")
	fmt.Println("  parse   在控制台打印 /api/dashboard_data 的后端解析结果")
	fmt.Println("  show    显示当前 mock 输入状态")
	fmt.Println("  clear   清除全部手动输入；也可 clear at / clear qca / clear qeng")
	fmt.Println("粘贴多行数据后，单独输入 .end 结束。")
	fmt.Println("==============================================================")
}
