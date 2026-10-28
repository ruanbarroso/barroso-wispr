//go:build windows

package main

import "errors"

func simularAtalho(string) error { return errors.New("só no macOS") }
