// SPDX-License-Identifier: AGPL-3.0-or-later

package wgsync

import (
	"strings"
	"testing"
	"time"
)

// realDump mirrors actual `wg show bamboo0 dump` output captured
// from the production VPS: one interface row + three peer rows.
const realDump = "PRIV1\tw+3xhUb6yQ7+lXiL0U9EYq+xPJJJCRnVHiWdISvgpnQ=\t51820\toff\n" +
	"8NiJUOCbpYS9MJXo7Sxzt9mjP6y7R94i6JK380IAjFs=\t(none)\t1.2.3.4:64903\t100.64.0.2/32\t1746876000\t1196112\t52340224\t25\n" +
	"uVxFSi4T59pu2j4yKubgJLCr0Fkl5R1ALoRTkwlCAHU=\t(none)\t1.2.3.4:49909\t100.64.0.3/32\t1746875940\t52326400\t1184800\t25\n" +
	"vjxTX0ELl2Yqpy2xzhDknnZ1+Sl/m8X6KAPHEodTGRw=\t(none)\t5.6.7.8:58500\t100.64.0.5/32\t0\t180\t92\t25\n"

func TestParseDump_RealVPSOutput(t *testing.T) {
	peers, err := ParseDump(strings.NewReader(realDump))
	if err != nil {
		t.Fatalf("ParseDump: %v", err)
	}
	if len(peers) != 3 {
		t.Fatalf("len(peers) = %d, want 3", len(peers))
	}
	if peers[0].PublicKey != "8NiJUOCbpYS9MJXo7Sxzt9mjP6y7R94i6JK380IAjFs=" {
		t.Errorf("peers[0].PublicKey = %q", peers[0].PublicKey)
	}
	if got := peers[0].LatestHandshake.Unix(); got != 1746876000 {
		t.Errorf("peers[0].LatestHandshake.Unix() = %d, want 1746876000", got)
	}
	// Last peer never handshook (sec=0); handshake time must be zero.
	if !peers[2].LatestHandshake.IsZero() {
		t.Errorf("peers[2].LatestHandshake = %v, want zero", peers[2].LatestHandshake)
	}
}

func TestParseDump_Empty(t *testing.T) {
	peers, err := ParseDump(strings.NewReader(""))
	if err != nil {
		t.Fatalf("ParseDump: %v", err)
	}
	if len(peers) != 0 {
		t.Errorf("len(peers) = %d, want 0", len(peers))
	}
}

func TestParseDump_OnlyInterfaceRow(t *testing.T) {
	body := "PRIV\tPUB\t51820\toff\n"
	peers, err := ParseDump(strings.NewReader(body))
	if err != nil {
		t.Fatalf("ParseDump: %v", err)
	}
	if len(peers) != 0 {
		t.Errorf("len(peers) = %d, want 0", len(peers))
	}
}

func TestParseDump_TrailingNewlines(t *testing.T) {
	body := realDump + "\n\n"
	peers, err := ParseDump(strings.NewReader(body))
	if err != nil {
		t.Fatalf("ParseDump: %v", err)
	}
	if len(peers) != 3 {
		t.Errorf("len(peers) = %d, want 3", len(peers))
	}
}

func TestParseDump_MalformedFieldCount(t *testing.T) {
	// 5 fields — neither 4 (interface) nor 8 (peer).
	body := "a\tb\tc\td\te\n"
	_, err := ParseDump(strings.NewReader(body))
	if err == nil {
		t.Fatal("ParseDump: want error, got nil")
	}
}

func TestParseDump_MalformedHandshake(t *testing.T) {
	body := "PRIV\tPUB\t51820\toff\n" +
		"key=\t(none)\t1.2.3.4:1\t100.64.0.2/32\tnotanumber\t0\t0\t25\n"
	_, err := ParseDump(strings.NewReader(body))
	if err == nil {
		t.Fatal("ParseDump: want error, got nil")
	}
}

func TestParseDump_NeverHandshookProducesZeroTime(t *testing.T) {
	body := "PRIV\tPUB\t51820\toff\n" +
		"newpeer=\t(none)\t1.2.3.4:1\t100.64.0.7/32\t0\t0\t0\t0\n"
	peers, err := ParseDump(strings.NewReader(body))
	if err != nil {
		t.Fatalf("ParseDump: %v", err)
	}
	if len(peers) != 1 {
		t.Fatalf("len(peers) = %d, want 1", len(peers))
	}
	if !peers[0].LatestHandshake.Equal(time.Time{}) {
		t.Errorf("LatestHandshake = %v, want zero", peers[0].LatestHandshake)
	}
}
