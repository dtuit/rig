package tunnel

import (
	"net"
	"testing"
	"time"
)

// A foreign listener on a requested local port would answer the connected
// probe and make exit 0 point at the wrong backend — waitPortFree is the
// pre-flight that refuses it.
func TestWaitPortFree(t *testing.T) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := l.Addr().(*net.TCPAddr).Port

	if err := waitPortFree(port, 50*time.Millisecond); err == nil {
		t.Error("held port reported free")
	}
	l.Close()
	if err := waitPortFree(port, time.Second); err != nil {
		t.Errorf("freed port still refused: %v", err)
	}
}
