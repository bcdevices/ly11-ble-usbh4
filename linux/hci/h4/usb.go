package h4

import (
	"context"
	"fmt"
	"io"
	"time"

	"github.com/google/gousb"
	"github.com/pkg/errors"
	"github.com/sirupsen/logrus"
)

func NewUsb(ctx *gousb.Context, usbVendorId, usbProductId uint16) (io.ReadWriteCloser, error) {
	logrus.Debugf("opening h4 usb...")

	fast := time.Millisecond * 500
	rwc, err := NewUsbRWC(ctx, fast, usbVendorId, usbProductId)
	if err != nil {
		return nil, errors.Wrap(err, "open")
	}

	eofAsError := true
	if err := resetAndWaitIdle(rwc, time.Second*2, eofAsError); err != nil {
		rwc.Close()
		return nil, errors.Wrap(err, "resetAndWaitIdle")
	}

	h := &h4{
		rwc:     rwc,
		done:    make(chan int),
		rxQueue: make(chan []byte, rxQueueSize),
		txQueue: make(chan []byte, txQueueSize),
	}
	h.frame = newFrame(h.rxQueue)

	go h.rxLoop(eofAsError)

	return h, nil
}

type usbRWC struct {
	ctx    *gousb.Context
	usbDev *gousb.Device
	intf   *gousb.Interface
	inEp   *gousb.InEndpoint
	outEp  *gousb.OutEndpoint

	timeout time.Duration
}

func (u *usbRWC) Read(p []byte) (int, error) {
	logrus.Debugf("usbRWC: read (buf:%d)...", len(p))

	opCtx := context.Background()

	opCtx, done := context.WithTimeout(opCtx, u.timeout)
	defer done()

	n, err := u.inEp.ReadContext(opCtx, p)
	logrus.Debugf("usbRWC: read complete.(n=%v, err=%v (%T))", n, err, err)

	if ts, ok := err.(gousb.TransferStatus); ok {
		if ts == gousb.TransferCancelled {
			return n, nil
		}
	}

	return n, err
}

func (u *usbRWC) Write(p []byte) (int, error) {
	logrus.Debugf("usbRWC: write(%v)", p)

	opCtx := context.Background()

	opCtx, done := context.WithTimeout(opCtx, u.timeout)
	defer done()

	n, err := u.outEp.WriteContext(opCtx, p)

	logrus.Debugf("usbRWC: write complete.")

	return n, err
}

func (u *usbRWC) Close() error {
	fmt.Printf("usbRWC: close\n")
	u.intf.Close()
	if err := u.usbDev.Close(); err != nil {
		return fmt.Errorf("close USB dev: %w", err)
	}
	return nil
}

func NewUsbRWC(ctx *gousb.Context, timeout time.Duration,
	usbVendorId, usbProductId uint16) (*usbRWC, error) {

	usbDev, err := ctx.OpenDeviceWithVIDPID(gousb.ID(usbVendorId),
		gousb.ID(usbProductId))
	if err != nil {
		return nil, fmt.Errorf("open USB device: %w", err)
	}

	if usbDev == nil {
		return nil, fmt.Errorf("device not found")
	}

	// Automatically detach any kernel driver and
	// reattach it when releasing the interface.
	err = usbDev.SetAutoDetach(true)
	if err != nil {
		_ = usbDev.Close()

		return nil, fmt.Errorf("set auto-detach: %w", err)
	}

	intf, _, err := usbDev.DefaultInterface()
	if err != nil {
		_ = usbDev.Close()

		return nil, fmt.Errorf("claim intf: %w", err)
	}

	inEp, err := intf.InEndpoint(0x81)
	if err != nil {
		intf.Close()
		_ = usbDev.Close()

		return nil, fmt.Errorf("claim in Ep: %w", err)
	}

	outEp, err := intf.OutEndpoint(0x01)
	if err != nil {
		intf.Close()
		_ = usbDev.Close()

		return nil, fmt.Errorf("claim out Ep: %w", err)
	}

	return &usbRWC{
		ctx:     ctx,
		usbDev:  usbDev,
		intf:    intf,
		inEp:    inEp,
		outEp:   outEp,
		timeout: timeout,
	}, nil
}
