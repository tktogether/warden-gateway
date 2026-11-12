package singbox

import (
	"bytes"
	"errors"

	qrcode "github.com/skip2/go-qrcode"
)

// maxQRText bounds the URI accepted by the QR encoder and the HTTP handler.
const maxQRText = 4096

// QRCodePNG renders one share URI as a PNG QR code of the requested size.
func QRCodePNG(text string, size int) ([]byte, error) {
	if text == "" {
		return nil, errors.New("内容为空")
	}
	if len(text) > maxQRText {
		return nil, errors.New("内容过长，无法生成二维码")
	}
	if size <= 0 || size > 1024 {
		size = 320
	}
	code, err := qrcode.New(text, qrcode.Medium)
	if err != nil {
		return nil, err
	}
	var buf bytes.Buffer
	if err := code.Write(size, &buf); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}
