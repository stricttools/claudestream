package claudestream

import (
	"os"
	"testing"

	"github.com/stricttools/claudestream/internal/vcr"
)

// TestMain makes the test binary the claude stand-in when the environment
// selects the cassette replay (internal/vcr), so the session tests start a
// real subprocess over real pipes.
func TestMain(m *testing.M) {
	if vcr.Active() {
		os.Exit(vcr.Main(os.Args[1:]))
	}
	os.Exit(m.Run())
}
