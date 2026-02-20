package main

import (
	"bytes"
	"compress/zlib"
	"encoding/binary"
	"fmt"
	"image"
	"image/color"
	imageDraw "image/draw"
	"image/jpeg"
	"image/png"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// StreamingPDFWriter는 이미지를 디코딩 없이 PDF에 직접 삽입하는
// 스트리밍 방식의 PDF 생성기입니다.
//
// 메모리 사용량:
//   - JPEG: raw bytes 패스스루 → 이미지 1장분 (~150KB)
//   - PNG (RGB/Gray): IDAT 청크 패스스루 → 압축 데이터 1장분 (~1-5MB)
//   - PNG (RGBA): 디코딩 필요 → ~35MB (폴백)
//   - 공통: xref 테이블 (~24B/페이지)
//
// 사용법:
//
//	w, _ := NewStreamingPDFWriter("output.pdf")
//	for _, path := range imagePaths {
//	    w.AddImagePage(path)  // JPEG/PNG 자동 감지
//	}
//	w.Close()
type StreamingPDFWriter struct {
	f        *os.File
	offset   int64
	xref     []int64 // 오브젝트별 바이트 오프셋 (인덱스 = 오브젝트번호-1)
	nextObj  int
	pageRefs []int // Page 오브젝트 번호 목록
}

// NewStreamingPDFWriter는 지정된 경로에 PDF 파일을 생성하고 writer를 반환합니다.
func NewStreamingPDFWriter(path string) (*StreamingPDFWriter, error) {
	f, err := os.Create(path)
	if err != nil {
		return nil, err
	}

	w := &StreamingPDFWriter{
		f:       f,
		nextObj: 3, // 1=Catalog, 2=Pages는 Close()에서 기록
	}

	header := "%PDF-1.4\n%\xc0\xc1\xc2\xc3\n"
	n, _ := f.WriteString(header)
	w.offset = int64(n)
	w.xref = make([]int64, 2, 4096)

	return w, nil
}

// ============================================================
// Public API
// ============================================================

// AddImagePage는 파일 확장자로 JPEG/PNG를 자동 감지하여 페이지를 추가합니다.
func (w *StreamingPDFWriter) AddImagePage(imagePath string) error {
	return w.AddImagePageWithDPI(imagePath, 300)
}

// AddImagePageWithDPI는 지정된 DPI로 이미지를 페이지로 추가합니다.
func (w *StreamingPDFWriter) AddImagePageWithDPI(imagePath string, dpi float64) error {
	switch strings.ToLower(filepath.Ext(imagePath)) {
	case ".jpg", ".jpeg":
		return w.AddJPEGPageWithDPI(imagePath, dpi)
	case ".png":
		return w.AddPNGPageWithDPI(imagePath, dpi)
	default:
		return fmt.Errorf("지원하지 않는 이미지 형식: %s", filepath.Ext(imagePath))
	}
}

// AddJPEGPage는 JPEG를 디코딩 없이 PDF 페이지로 추가합니다. (300 DPI)
func (w *StreamingPDFWriter) AddJPEGPage(jpegPath string) error {
	return w.AddJPEGPageWithDPI(jpegPath, 300)
}

// AddJPEGPageWithDPI는 지정된 DPI로 JPEG를 PDF 페이지로 추가합니다.
// JPEG raw bytes → DCTDecode 필터로 직접 삽입. 디코딩 없음.
func (w *StreamingPDFWriter) AddJPEGPageWithDPI(jpegPath string, dpi float64) error {
	info, err := readJPEGInfo(jpegPath)
	if err != nil {
		return fmt.Errorf("JPEG 정보 읽기 실패 (%s): %w", jpegPath, err)
	}

	jpegData, err := os.ReadFile(jpegPath)
	if err != nil {
		return fmt.Errorf("JPEG 읽기 실패 (%s): %w", jpegPath, err)
	}

	widthPts := float64(info.width) * 72.0 / dpi
	heightPts := float64(info.height) * 72.0 / dpi

	imgObj := w.nextObj
	w.recordOffset(imgObj)
	w.writef("%d 0 obj\n<< /Type /XObject /Subtype /Image /Width %d /Height %d /ColorSpace /%s /BitsPerComponent 8 /Filter /DCTDecode /Length %d >>\nstream\n",
		imgObj, info.width, info.height, info.colorSpace, len(jpegData))
	w.writeBytes(jpegData)
	w.writef("\nendstream\nendobj\n")
	w.nextObj++

	jpegData = nil

	w.writeContentAndPage(imgObj, widthPts, heightPts)
	return nil
}

// AddPNGPage는 PNG를 PDF 페이지로 추가합니다. (300 DPI)
// RGB/Grayscale(비인터레이스): IDAT 청크를 직접 패스스루 (디코딩 없음)
// RGBA/기타: 디코딩 후 RGB 추출하여 압축 (폴백)
func (w *StreamingPDFWriter) AddPNGPage(pngPath string) error {
	return w.AddPNGPageWithDPI(pngPath, 300)
}

// AddPNGPageWithDPI는 지정된 DPI로 PNG를 PDF 페이지로 추가합니다.
func (w *StreamingPDFWriter) AddPNGPageWithDPI(pngPath string, dpi float64) error {
	// 직접 패스스루 시도 (최적 경로)
	pd, err := parsePNGDirect(pngPath)
	if err == nil {
		return w.addPNGDirect(pd, dpi)
	}

	// 폴백: 디코딩 후 재압축
	return w.addPNGDecoded(pngPath, dpi)
}

// PageCount는 현재까지 추가된 페이지 수를 반환합니다.
func (w *StreamingPDFWriter) PageCount() int {
	return len(w.pageRefs)
}

// Close는 PDF 구조를 마무리하고 파일을 닫습니다.
// 반드시 호출해야 유효한 PDF 파일이 생성됩니다.
func (w *StreamingPDFWriter) Close() error {
	// Pages (오브젝트 2)
	w.xref[1] = w.offset
	kids := ""
	for _, ref := range w.pageRefs {
		kids += fmt.Sprintf("%d 0 R ", ref)
	}
	w.writef("2 0 obj\n<< /Type /Pages /Kids [%s] /Count %d >>\nendobj\n",
		kids, len(w.pageRefs))

	// Catalog (오브젝트 1)
	w.xref[0] = w.offset
	w.writef("1 0 obj\n<< /Type /Catalog /Pages 2 0 R >>\nendobj\n")

	// xref 테이블
	xrefOffset := w.offset
	totalObjects := len(w.xref) + 1
	w.writef("xref\n0 %d\n", totalObjects)
	w.writef("%010d %05d f \r\n", 0, 65535)
	for _, off := range w.xref {
		w.writef("%010d %05d n \r\n", off, 0)
	}

	// trailer
	w.writef("trailer\n<< /Size %d /Root 1 0 R >>\nstartxref\n%d\n%%%%EOF\n",
		totalObjects, xrefOffset)

	return w.f.Close()
}

// ============================================================
// PNG 직접 패스스루 (디코딩 없음)
// ============================================================
//
// PNG 파일 구조:
//   [시그니처] → [IHDR 청크] → [IDAT 청크들...] → [IEND]
//
// IDAT 안의 데이터 = zlib 압축된 (필터바이트 + 픽셀데이터) 행들
// PDF의 /FlateDecode + /Predictor 15 가 이것과 동일한 포맷
// → IDAT 데이터를 그대로 PDF stream에 넣으면 됨
//

type pngDirectData struct {
	width, height int
	bitDepth      int
	colorType     int // 0=Gray, 2=RGB
	colorSpace    string
	colors        int // 색상 컴포넌트 수 (1 or 3)
	idatData      []byte
}

// parsePNGDirect는 PNG 파일의 청크를 파싱하여 IDAT 데이터를 추출합니다.
// 비인터레이스 + 8bit + RGB/Grayscale만 지원. 그 외는 에러 반환.
func parsePNGDirect(path string) (*pngDirectData, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	// PNG 시그니처 검증
	var sig [8]byte
	if _, err := io.ReadFull(f, sig[:]); err != nil {
		return nil, err
	}
	if sig != [8]byte{0x89, 0x50, 0x4E, 0x47, 0x0D, 0x0A, 0x1A, 0x0A} {
		return nil, fmt.Errorf("PNG 시그니처 불일치")
	}

	var data pngDirectData
	var idatBuf []byte

	for {
		// 청크 헤더: 길이(4) + 타입(4)
		var chunkLen uint32
		if err := binary.Read(f, binary.BigEndian, &chunkLen); err != nil {
			return nil, fmt.Errorf("청크 읽기 실패: %w", err)
		}

		var chunkType [4]byte
		if _, err := io.ReadFull(f, chunkType[:]); err != nil {
			return nil, err
		}

		switch string(chunkType[:]) {
		case "IHDR":
			ihdr := make([]byte, chunkLen)
			if _, err := io.ReadFull(f, ihdr); err != nil {
				return nil, err
			}
			data.width = int(binary.BigEndian.Uint32(ihdr[0:4]))
			data.height = int(binary.BigEndian.Uint32(ihdr[4:8]))
			data.bitDepth = int(ihdr[8])
			data.colorType = int(ihdr[9])
			interlace := ihdr[12]

			// 직접 패스스루 가능 조건 체크
			if interlace != 0 {
				return nil, fmt.Errorf("인터레이스 PNG는 패스스루 불가")
			}
			if data.bitDepth != 8 {
				return nil, fmt.Errorf("bit depth %d는 패스스루 불가", data.bitDepth)
			}
			switch data.colorType {
			case 0: // Grayscale
				data.colorSpace = "DeviceGray"
				data.colors = 1
			case 2: // RGB
				data.colorSpace = "DeviceRGB"
				data.colors = 3
			default:
				return nil, fmt.Errorf("color type %d는 패스스루 불가 (RGBA등)", data.colorType)
			}

			f.Seek(4, io.SeekCurrent) // CRC 건너뛰기

		case "IDAT":
			chunk := make([]byte, chunkLen)
			if _, err := io.ReadFull(f, chunk); err != nil {
				return nil, err
			}
			idatBuf = append(idatBuf, chunk...)
			f.Seek(4, io.SeekCurrent) // CRC

		case "IEND":
			data.idatData = idatBuf
			return &data, nil

		default:
			// 기타 청크 건너뛰기 (PLTE, tEXt 등)
			f.Seek(int64(chunkLen)+4, io.SeekCurrent)
		}
	}
}

// addPNGDirect는 파싱된 IDAT 데이터를 PDF에 직접 삽입합니다.
func (w *StreamingPDFWriter) addPNGDirect(pd *pngDirectData, dpi float64) error {
	widthPts := float64(pd.width) * 72.0 / dpi
	heightPts := float64(pd.height) * 72.0 / dpi

	// Image XObject (IDAT zlib 데이터 → FlateDecode + PNG Predictor)
	imgObj := w.nextObj
	w.recordOffset(imgObj)
	w.writef("%d 0 obj\n<< /Type /XObject /Subtype /Image /Width %d /Height %d "+
		"/ColorSpace /%s /BitsPerComponent %d "+
		"/Filter /FlateDecode "+
		"/DecodeParms << /Predictor 15 /Colors %d /BitsPerComponent %d /Columns %d >> "+
		"/Length %d >>\nstream\n",
		imgObj, pd.width, pd.height, pd.colorSpace, pd.bitDepth,
		pd.colors, pd.bitDepth, pd.width, len(pd.idatData))
	w.writeBytes(pd.idatData)
	w.writef("\nendstream\nendobj\n")
	w.nextObj++

	pd.idatData = nil

	w.writeContentAndPage(imgObj, widthPts, heightPts)
	return nil
}

// ============================================================
// PNG 폴백 (RGBA, 인터레이스 등 → 디코딩 후 재압축)
// ============================================================

// addPNGDecoded는 PNG를 완전히 디코딩한 뒤 RGB 픽셀을 추출하여 PDF에 삽입합니다.
// 메모리: 디코딩된 이미지 (~35MB) + 압축 버퍼 (가변)
func (w *StreamingPDFWriter) addPNGDecoded(pngPath string, dpi float64) error {
	f, err := os.Open(pngPath)
	if err != nil {
		return fmt.Errorf("PNG 열기 실패: %w", err)
	}

	img, err := png.Decode(f)
	f.Close()
	if err != nil {
		return fmt.Errorf("PNG 디코딩 실패: %w", err)
	}

	bounds := img.Bounds()
	width := bounds.Dx()
	height := bounds.Dy()

	// 어떤 이미지 타입이든 NRGBA로 변환하여 픽셀 직접 접근
	nrgba := image.NewNRGBA(bounds)
	imageDraw.Draw(nrgba, bounds, img, bounds.Min, imageDraw.Src)
	img = nil

	// RGB 픽셀을 행 단위로 zlib 압축 (알파 채널 제거)
	var buf bytes.Buffer
	zw, _ := zlib.NewWriterLevel(&buf, zlib.BestSpeed)
	row := make([]byte, width*3)

	for y := 0; y < height; y++ {
		off := y * nrgba.Stride
		for x := 0; x < width; x++ {
			row[x*3] = nrgba.Pix[off]     // R
			row[x*3+1] = nrgba.Pix[off+1] // G
			row[x*3+2] = nrgba.Pix[off+2] // B
			off += 4                        // A 건너뛰기
		}
		zw.Write(row)
	}
	zw.Close()
	nrgba = nil

	compressed := buf.Bytes()
	widthPts := float64(width) * 72.0 / dpi
	heightPts := float64(height) * 72.0 / dpi

	// Image XObject (FlateDecode, predictor 없음)
	imgObj := w.nextObj
	w.recordOffset(imgObj)
	w.writef("%d 0 obj\n<< /Type /XObject /Subtype /Image /Width %d /Height %d "+
		"/ColorSpace /DeviceRGB /BitsPerComponent 8 "+
		"/Filter /FlateDecode /Length %d >>\nstream\n",
		imgObj, width, height, len(compressed))
	w.writeBytes(compressed)
	w.writef("\nendstream\nendobj\n")
	w.nextObj++

	compressed = nil

	w.writeContentAndPage(imgObj, widthPts, heightPts)
	return nil
}

// ============================================================
// 공통 내부 함수
// ============================================================

// writeContentAndPage는 Content Stream과 Page 오브젝트를 기록합니다.
func (w *StreamingPDFWriter) writeContentAndPage(imgObjNum int, widthPts, heightPts float64) {
	// Content Stream
	content := fmt.Sprintf("q %.2f 0 0 %.2f 0 0 cm /Img0 Do Q", widthPts, heightPts)
	contentObj := w.nextObj
	w.recordOffset(contentObj)
	w.writef("%d 0 obj\n<< /Length %d >>\nstream\n%s\nendstream\nendobj\n",
		contentObj, len(content), content)
	w.nextObj++

	// Page
	pageObj := w.nextObj
	w.recordOffset(pageObj)
	w.writef("%d 0 obj\n<< /Type /Page /Parent 2 0 R /MediaBox [0 0 %.2f %.2f] /Contents %d 0 R /Resources << /XObject << /Img0 %d 0 R >> >> >>\nendobj\n",
		pageObj, widthPts, heightPts, contentObj, imgObjNum)
	w.nextObj++

	w.pageRefs = append(w.pageRefs, pageObj)
}

type jpegInfo struct {
	width, height int
	colorSpace    string
}

func readJPEGInfo(path string) (jpegInfo, error) {
	f, err := os.Open(path)
	if err != nil {
		return jpegInfo{}, err
	}
	defer f.Close()

	cfg, err := jpeg.DecodeConfig(f)
	if err != nil {
		return jpegInfo{}, err
	}

	cs := "DeviceRGB"
	if cfg.ColorModel == color.GrayModel {
		cs = "DeviceGray"
	}
	return jpegInfo{width: cfg.Width, height: cfg.Height, colorSpace: cs}, nil
}

func (w *StreamingPDFWriter) recordOffset(objNum int) {
	for len(w.xref) < objNum {
		w.xref = append(w.xref, 0)
	}
	w.xref[objNum-1] = w.offset
}

func (w *StreamingPDFWriter) writef(format string, args ...any) {
	s := fmt.Sprintf(format, args...)
	n, _ := w.f.WriteString(s)
	w.offset += int64(n)
}

func (w *StreamingPDFWriter) writeBytes(data []byte) {
	n, _ := w.f.Write(data)
	w.offset += int64(n)
}
