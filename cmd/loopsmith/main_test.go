package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestInitDoesNotOverwrite(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	if e := run(context.Background(), []string{"init", "--config", path}); e != nil {
		t.Fatal(e)
	}
	before, _ := os.ReadFile(path)
	if e := run(context.Background(), []string{"init", "--config", path}); e == nil {
		t.Fatal("overwrote existing config")
	}
	after, _ := os.ReadFile(path)
	if string(before) != string(after) {
		t.Fatal("config changed")
	}
}
func TestRunRequiresTask(t *testing.T) {
	if e := run(context.Background(), []string{"run"}); e == nil || !strings.Contains(e.Error(), "--task") {
		t.Fatalf("expected task error: %v", e)
	}
}
func TestUnknownCommand(t *testing.T) {
	if e := run(context.Background(), []string{"missing"}); e == nil {
		t.Fatal("unknown command succeeded")
	}
}
func TestResumeRequiresDirectory(t *testing.T) {
	if e := run(context.Background(), []string{"resume"}); e == nil {
		t.Fatal("missing directory succeeded")
	}
}
