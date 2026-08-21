//go:build !windows

package main

// holdWindowIfLaunchedByClick exists only for Windows, where a double-clicked
// program takes its window down with it. Everywhere else the shell keeps the
// output on screen.
func holdWindowIfLaunchedByClick() {}
