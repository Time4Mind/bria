package runtimehost

import "testing"

func TestLocalExecutorCaptureUsesTheSameSessionFIFO(t *testing.T) {
	driver := &fakeRuntimeDriver{pane: []byte("\x1b[31mhello\x1b[0m")}
	executor := newTestExecutor(t, driver)
	request := testRequest("capture-1", ActionCapture)
	result := waitSubmittedResult(t, executor, request)
	if string(result.Pane) != string(driver.pane) {
		t.Fatalf("pane = %q", result.Pane)
	}
}
