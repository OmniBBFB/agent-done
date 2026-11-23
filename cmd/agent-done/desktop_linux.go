package main

import "fmt"

func macDesktop(name string) (desktopBackend, error) {
	return desktopBackend{}, fmt.Errorf("Linux 不支持 macOS 桌面后端 %q", name)
}
