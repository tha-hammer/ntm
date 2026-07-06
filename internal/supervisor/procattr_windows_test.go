//go:build windows

package supervisor

import (
	"errors"
	"os"
	"os/exec"
	"syscall"
	"testing"

	"golang.org/x/sys/windows"
)

func TestSetSysProcAttrCreatesWindowsProcessGroup(t *testing.T) {
	cmd := exec.Command("cmd", "/c", "exit", "0")

	setSysProcAttr(cmd)

	if cmd.SysProcAttr == nil {
		t.Fatal("setSysProcAttr left SysProcAttr nil")
	}
	if got := cmd.SysProcAttr.CreationFlags; got&windows.CREATE_NEW_PROCESS_GROUP == 0 {
		t.Fatalf("CreationFlags = %#x, missing CREATE_NEW_PROCESS_GROUP", got)
	}
}

func TestSetSysProcAttrPreservesExistingCreationFlags(t *testing.T) {
	cmd := exec.Command("cmd", "/c", "exit", "0")
	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: windows.CREATE_NO_WINDOW}

	setSysProcAttr(cmd)

	got := cmd.SysProcAttr.CreationFlags
	if got&windows.CREATE_NO_WINDOW == 0 {
		t.Fatalf("CreationFlags = %#x, missing pre-existing CREATE_NO_WINDOW", got)
	}
	if got&windows.CREATE_NEW_PROCESS_GROUP == 0 {
		t.Fatalf("CreationFlags = %#x, missing CREATE_NEW_PROCESS_GROUP", got)
	}
}

func TestTerminateProcessSendsCtrlBreakToProcessGroup(t *testing.T) {
	restore := stubWindowsTermination(t)
	defer restore()

	proc := &os.Process{Pid: 4321}
	terminateProcess(proc)

	if got, want := terminationStub.ctrlEvent, uint32(windows.CTRL_BREAK_EVENT); got != want {
		t.Fatalf("control event = %d, want CTRL_BREAK_EVENT", got)
	}
	if got, want := terminationStub.groupID, uint32(proc.Pid); got != want {
		t.Fatalf("process group ID = %d, want child PID %d", got, want)
	}
	if terminationStub.killCalls != 0 {
		t.Fatalf("kill fallback called %d time(s) after successful CTRL_BREAK_EVENT", terminationStub.killCalls)
	}
}

func TestTerminateProcessFallsBackToKillWhenCtrlBreakFails(t *testing.T) {
	restore := stubWindowsTermination(t)
	defer restore()
	terminationStub.ctrlErr = errors.New("console control event unavailable")

	proc := &os.Process{Pid: 9876}
	terminateProcess(proc)

	if terminationStub.killCalls != 1 {
		t.Fatalf("kill fallback calls = %d, want 1", terminationStub.killCalls)
	}
	if terminationStub.killedProcess != proc {
		t.Fatal("kill fallback did not receive the original process")
	}
}

func TestTerminateProcessNilIsNoop(t *testing.T) {
	restore := stubWindowsTermination(t)
	defer restore()

	terminateProcess(nil)

	if terminationStub.ctrlCalls != 0 {
		t.Fatalf("control event calls = %d, want 0", terminationStub.ctrlCalls)
	}
	if terminationStub.killCalls != 0 {
		t.Fatalf("kill fallback calls = %d, want 0", terminationStub.killCalls)
	}
}

var terminationStub struct {
	ctrlCalls     int
	ctrlEvent     uint32
	groupID       uint32
	ctrlErr       error
	killCalls     int
	killedProcess *os.Process
	killErr       error
}

func stubWindowsTermination(t *testing.T) func() {
	t.Helper()
	terminationStub = struct {
		ctrlCalls     int
		ctrlEvent     uint32
		groupID       uint32
		ctrlErr       error
		killCalls     int
		killedProcess *os.Process
		killErr       error
	}{}

	origGenerate := generateConsoleCtrlEvent
	origKill := killProcess
	generateConsoleCtrlEvent = func(ctrlEvent uint32, processGroupID uint32) error {
		terminationStub.ctrlCalls++
		terminationStub.ctrlEvent = ctrlEvent
		terminationStub.groupID = processGroupID
		return terminationStub.ctrlErr
	}
	killProcess = func(p *os.Process) error {
		terminationStub.killCalls++
		terminationStub.killedProcess = p
		return terminationStub.killErr
	}

	return func() {
		generateConsoleCtrlEvent = origGenerate
		killProcess = origKill
	}
}
