package service

import "testing"

func TestParseJournalUSBSource(t *testing.T) {
	if e, ok := ParseJournal(j("[mixer] event=usb2 source=\"Cam Link 4K\"")); !ok || e.Kind != "usb2" || e.Level != "warn" {
		t.Errorf("usb2: %+v", e)
	}
	// NVIDIA's noise when the MJPEG decoder starts
	if _, ok := ParseJournal(j("NVMEDIA: Reading vendor.tegra.display-size : status: 6")); ok {
		t.Error("NVMEDIA noise should be filtered")
	}
}
