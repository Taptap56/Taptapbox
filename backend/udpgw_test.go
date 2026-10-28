// udpgw_test.go — framing protokol BadVPN udpgw + client echo.

package main

import (
	"bytes"
	"encoding/binary"
	"net"
	"testing"
	"time"
)

func TestUdpgwEncodeDecodeIPv4(t *testing.T) {
	msg, err := encodeUdpgwMessage(0, 1234, net.ParseIP("1.2.3.4"), 27015, []byte("hello"))
	if err != nil {
		t.Fatal(err)
	}
	// size LE = 3 + 4 + 2 + 5 = 14.
	if got := int(binary.LittleEndian.Uint16(msg[0:2])); got != 14 {
		t.Fatalf("size = %d, mau 14", got)
	}
	m, err := decodeUdpgwMessage(msg[2:])
	if err != nil {
		t.Fatal(err)
	}
	if m.Flags != 0 || m.ConnID != 1234 || m.IP.String() != "1.2.3.4" || m.Port != 27015 || string(m.Payload) != "hello" {
		t.Fatalf("decode salah: %+v", m)
	}
}

func TestUdpgwEncodeDecodeIPv6(t *testing.T) {
	msg, err := encodeUdpgwMessage(0, 7, net.ParseIP("2001:db8::1"), 443, []byte("x"))
	if err != nil {
		t.Fatal(err)
	}
	m, err := decodeUdpgwMessage(msg[2:])
	if err != nil {
		t.Fatal(err)
	}
	if m.Flags&udpgwFlagIPv6 == 0 || m.IP.String() != "2001:db8::1" || m.Port != 443 {
		t.Fatalf("decode IPv6 salah: %+v", m)
	}
}

func TestUdpgwKeepalive(t *testing.T) {
	ka := encodeUdpgwKeepalive()
	if len(ka) != 5 || ka[0] != 0x03 || ka[1] != 0x00 || ka[2] != udpgwFlagKeepalive {
		t.Fatalf("keepalive salah: %x", ka)
	}
	m, err := decodeUdpgwMessage(ka[2:])
	if err != nil {
		t.Fatal(err)
	}
	if m.Flags&udpgwFlagKeepalive == 0 {
		t.Fatalf("keepalive tidak dikenali: %+v", m)
	}
}

func TestUdpgwDecodeErrors(t *testing.T) {
	if _, err := decodeUdpgwMessage([]byte{0x00, 0x00}); err == nil {
		t.Fatal("pesan 2 byte harus error")
	}
	// IPv6 flag tapi alamat terpotong.
	if _, err := decodeUdpgwMessage([]byte{udpgwFlagIPv6, 0x01, 0x00, 1, 2, 3}); err == nil {
		t.Fatal("alamat IPv6 terpotong harus error")
	}
	// readUdpgwMessage: size tidak valid.
	if _, err := readUdpgwMessage(bytes.NewReader([]byte{0x02, 0x00, 0x00, 0x00, 0x00})); err == nil {
		t.Fatal("size 2 harus error")
	}
	if _, err := readUdpgwMessage(bytes.NewReader([]byte{0xff, 0xff})); err == nil {
		t.Fatal("size raksasa harus error")
	}
	// Payload melebihi batas harus ditolak saat encode.
	big := make([]byte, udpgwMaxPayload+1)
	if _, err := encodeUdpgwMessage(0, 1, net.ParseIP("1.1.1.1"), 53, big); err == nil {
		t.Fatal("payload >32768 harus error")
	}
}

func TestUdpgwClientEcho(t *testing.T) {
	cli, srv := net.Pipe()
	// Server tiruan: baca pesan, balas dengan connID + alamat tujuan yang
	// sama dan payload yang sama (seolah peer UDP membalas).
	go func() {
		for {
			m, err := readUdpgwMessage(srv)
			if err != nil {
				return
			}
			if m.Flags&udpgwFlagKeepalive != 0 {
				continue
			}
			resp, err := encodeUdpgwMessage(0, m.ConnID, m.IP, m.Port, m.Payload)
			if err != nil {
				return
			}
			if _, err := srv.Write(resp); err != nil {
				return
			}
		}
	}()
	got := make(chan udpgwMessage, 1)
	cl := newUdpgwClient(cli, func(m udpgwMessage) { got <- m }, func(err error) {})
	go cl.run()
	defer cl.close()
	if err := cl.send(0, 42, net.ParseIP("8.8.4.4"), 9999, []byte("ping")); err != nil {
		t.Fatal(err)
	}
	select {
	case m := <-got:
		if m.ConnID != 42 || m.IP.String() != "8.8.4.4" || m.Port != 9999 || string(m.Payload) != "ping" {
			t.Fatalf("echo salah: %+v", m)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("tidak ada balasan echo")
	}
}
