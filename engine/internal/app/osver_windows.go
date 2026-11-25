//go:build windows

package app

import "golang.org/x/sys/windows"

func osVersion() string {
	v := windows.RtlGetVersion()
	return "Windows " + itoa(int(v.MajorVersion)) + "." + itoa(int(v.MinorVersion)) + " build " + itoa(int(v.BuildNumber))
}
