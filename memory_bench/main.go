package main

import (
	"fmt"
	"image"
	"image/color"
	"image/jpeg"
	"os"
	"path/filepath"
	"runtime"
	"time"

	"github.com/pdfcpu/pdfcpu/pkg/api"
)

const (
	imgWidth    = 2480
	imgHeight   = 3508
	totalPages  = 1000
	insertCount = 100
	insertAt    = 500
)

func main() {
	fmt.Println("============================================")
	fmt.Println("  메모리 사용량 비교 벤치마크")
	fmt.Println("============================================")
	fmt.Printf("총 페이지: %d (기존 %d + 삽입 %d)\n", totalPages+insertCount, totalPages, insertCount)
	fmt.Printf("이미지 크기: %dx%d px\n\n", imgWidth, imgHeight)

	tmpDir, _ := os.MkdirTemp("", "mem_bench_*")
	defer os.RemoveAll(tmpDir)

	// 셋업: 테스트 이미지 1100개 생성
	fmt.Println("--- 셋업 ---")
	imgDir := filepath.Join(tmpDir, "images")
	os.MkdirAll(imgDir, 0755)
	allImages := generateTestImages(imgDir, totalPages+insertCount)
	fmt.Printf("  이미지 %d개 준비 완료\n\n", len(allImages))

	// 이미지 순서: 앞 500 + 삽입 100 + 뒤 500
	ordered := make([]string, 0, totalPages+insertCount)
	ordered = append(ordered, allImages[:insertAt]...)
	ordered = append(ordered, allImages[totalPages:]...)
	ordered = append(ordered, allImages[insertAt:totalPages]...)

	// ==================================================
	// Method A: pdfcpu ImportImagesFile (일괄 처리)
	// ==================================================
	fmt.Println("--- Method A: pdfcpu ImportImagesFile (일괄) ---")
	runtime.GC()
	memBefore := getAlloc()
	fmt.Printf("  시작 메모리: %.1f MB\n", memBefore)

	startA := time.Now()
	outputA := filepath.Join(tmpDir, "output_pdfcpu.pdf")
	if err := api.ImportImagesFile(ordered, outputA, nil, nil); err != nil {
		fmt.Fprintf(os.Stderr, "  실패: %v\n", err)
		os.Exit(1)
	}
	elapsedA := time.Since(startA)

	memAfterA := getAlloc()
	runtime.GC()
	memAfterGC_A := getAlloc()
	fiA, _ := os.Stat(outputA)
	pagesA, _ := api.PageCountFile(outputA)

	fmt.Printf("  완료: %v\n", elapsedA)
	fmt.Printf("  결과: %.1f MB, %d 페이지\n", float64(fiA.Size())/(1024*1024), pagesA)
	fmt.Printf("  GC 전 메모리: %.1f MB (증가: %.1f MB)\n", memAfterA, memAfterA-memBefore)
	fmt.Printf("  GC 후 메모리: %.1f MB\n\n", memAfterGC_A)

	// GC로 정리
	runtime.GC()

	// ==================================================
	// Method B: 스트리밍 PDF Writer (1장씩 순차 처리)
	// ==================================================
	fmt.Println("--- Method B: 스트리밍 PDF Writer (1장씩) ---")
	runtime.GC()
	memBefore = getAlloc()
	fmt.Printf("  시작 메모리: %.1f MB\n", memBefore)

	startB := time.Now()
	outputB := filepath.Join(tmpDir, "output_streaming.pdf")
	peakB := memBefore

	writer, err := NewStreamingPDFWriter(outputB)
	if err != nil {
		fmt.Fprintf(os.Stderr, "  Writer 생성 실패: %v\n", err)
		os.Exit(1)
	}

	for i, imgPath := range ordered {
		if err := writer.AddJPEGPage(imgPath); err != nil {
			fmt.Fprintf(os.Stderr, "  페이지 %d 추가 실패: %v\n", i+1, err)
			os.Exit(1)
		}
		// 100장마다 메모리 측정
		if (i+1)%100 == 0 {
			current := getAlloc()
			if current > peakB {
				peakB = current
			}
			fmt.Printf("  [%4d/%d] 메모리: %.1f MB\n", i+1, len(ordered), current)
		}
	}

	if err := writer.Close(); err != nil {
		fmt.Fprintf(os.Stderr, "  PDF 마무리 실패: %v\n", err)
		os.Exit(1)
	}
	elapsedB := time.Since(startB)

	memAfterB := getAlloc()
	if memAfterB > peakB {
		peakB = memAfterB
	}
	runtime.GC()
	memAfterGC_B := getAlloc()
	fiB, _ := os.Stat(outputB)

	fmt.Printf("  완료: %v\n", elapsedB)
	fmt.Printf("  결과: %.1f MB, %d 페이지\n", float64(fiB.Size())/(1024*1024), len(ordered))
	fmt.Printf("  피크 메모리: %.1f MB (증가: %.1f MB)\n", peakB, peakB-memBefore)
	fmt.Printf("  GC 후 메모리: %.1f MB\n\n", memAfterGC_B)

	// ==================================================
	// 결과 비교
	// ==================================================
	fmt.Println("============================================")
	fmt.Println("  결과 비교")
	fmt.Println("============================================")
	fmt.Printf("                   Method A (pdfcpu)    Method B (스트리밍)\n")
	fmt.Printf("  소요 시간:       %-20v %v\n", elapsedA, elapsedB)
	fmt.Printf("  GC전 메모리증가: %-20.1f MB %.1f MB\n", memAfterA-memBefore, peakB-memBefore)
	fmt.Printf("  결과 파일:       %-20.1f MB %.1f MB\n",
		float64(fiA.Size())/(1024*1024), float64(fiB.Size())/(1024*1024))
	fmt.Println()

	memDiff := (memAfterA - memBefore) - (peakB - memBefore)
	if memDiff > 0 {
		fmt.Printf(">> 스트리밍 방식이 메모리 %.1f MB 절약 (%.0f%% 감소)\n",
			memDiff, memDiff/(memAfterA-memBefore)*100)
	}

	fmt.Println()
	fmt.Println("============================================")
	fmt.Println("  GCS 연동 시 권장 패턴")
	fmt.Println("============================================")
	fmt.Println(`
  writer := NewStreamingPDFWriter("output.pdf")

  for _, key := range gcsImageKeys {
      // 1. GCS에서 로컬 임시파일로 다운로드
      tmpFile := download(bucket, key)

      // 2. PDF에 페이지 추가 (JPEG raw bytes만 읽음, 디코딩 없음)
      writer.AddJPEGPage(tmpFile)

      // 3. 임시파일 즉시 삭제 → 디스크도 최소 사용
      os.Remove(tmpFile)
  }

  writer.Close()

  메모리: 이미지 1장분 (~150KB) + xref 테이블 (~24B × 페이지수)
  1100페이지 기준: ~0.2 MB (vs pdfcpu ~160+ MB)`)
}

// ============================================================
// StreamingPDFWriter
// ============================================================
// JPEG raw bytes를 디코딩 없이 PDF에 직접 삽입합니다.
// 한 번에 이미지 1장만 메모리에 올리고, 즉시 디스크에 기록합니다.
//
// 메모리 사용량:
//   - 이미지 1장의 JPEG 바이트 (~150KB)
//   - xref 오프셋 테이블 (int64 × 오브젝트 수, ~24B/페이지)
//   - 페이지 참조 목록 (int × 페이지 수, ~8B/페이지)
//   - 총: 1100페이지 기준 약 0.2 MB
// ============================================================

type StreamingPDFWriter struct {
	f       *os.File
	offset  int64
	xref    []int64 // 각 오브젝트의 바이트 오프셋 (인덱스 = 오브젝트번호 - 1)
	nextObj int     // 다음 할당할 오브젝트 번호
	pageRefs []int  // Page 오브젝트 번호 목록
}

func NewStreamingPDFWriter(path string) (*StreamingPDFWriter, error) {
	f, err := os.Create(path)
	if err != nil {
		return nil, err
	}

	w := &StreamingPDFWriter{
		f:       f,
		nextObj: 3, // 1=Catalog, 2=Pages → 나중에 기록
	}

	// PDF 헤더 + 바이너리 마커
	header := "%PDF-1.4\n%\xc0\xc1\xc2\xc3\n"
	n, _ := f.WriteString(header)
	w.offset = int64(n)

	// 오브젝트 1(Catalog), 2(Pages) 자리 예약
	w.xref = make([]int64, 2, 4096) // 나중에 채움

	return w, nil
}

// AddJPEGPage: JPEG 파일을 디코딩 없이 PDF 페이지로 추가
func (w *StreamingPDFWriter) AddJPEGPage(jpegPath string) error {
	// JPEG 헤더만 읽어서 크기/색공간 파악 (디코딩 없음)
	info, err := readJPEGInfo(jpegPath)
	if err != nil {
		return fmt.Errorf("JPEG 정보 읽기 실패: %w", err)
	}

	// JPEG raw bytes 읽기
	jpegData, err := os.ReadFile(jpegPath)
	if err != nil {
		return fmt.Errorf("JPEG 읽기 실패: %w", err)
	}

	// 오브젝트 1: Image XObject (JPEG raw bytes 직접 삽입)
	imgObjNum := w.nextObj
	w.writeObjStart(imgObjNum)
	imgDict := fmt.Sprintf("<< /Type /XObject /Subtype /Image /Width %d /Height %d /ColorSpace /%s /BitsPerComponent 8 /Filter /DCTDecode /Length %d >>\nstream\n",
		info.Width, info.Height, info.ColorSpace, len(jpegData))
	w.writeRaw([]byte(imgDict))
	w.writeRaw(jpegData)
	w.writeRaw([]byte("\nendstream\nendobj\n"))
	w.nextObj++

	// jpegData 해제 (GC가 수거할 수 있도록)
	jpegData = nil

	// 오브젝트 2: Content Stream (이미지 그리기 명령)
	widthPts := float64(info.Width) * 72.0 / 300.0
	heightPts := float64(info.Height) * 72.0 / 300.0
	content := fmt.Sprintf("q %.2f 0 0 %.2f 0 0 cm /Img0 Do Q", widthPts, heightPts)

	contentObjNum := w.nextObj
	w.writeObjStart(contentObjNum)
	w.writeRaw([]byte(fmt.Sprintf("<< /Length %d >>\nstream\n%s\nendstream\nendobj\n", len(content), content)))
	w.nextObj++

	// 오브젝트 3: Page 오브젝트
	pageObjNum := w.nextObj
	w.writeObjStart(pageObjNum)
	pageDict := fmt.Sprintf("<< /Type /Page /Parent 2 0 R /MediaBox [0 0 %.2f %.2f] /Contents %d 0 R /Resources << /XObject << /Img0 %d 0 R >> >> >>\nendobj\n",
		widthPts, heightPts, contentObjNum, imgObjNum)
	w.writeRaw([]byte(pageDict))
	w.nextObj++

	w.pageRefs = append(w.pageRefs, pageObjNum)
	return nil
}

// Close: Pages, Catalog, xref 테이블, trailer 기록 후 파일 닫기
func (w *StreamingPDFWriter) Close() error {
	// Pages 오브젝트 (오브젝트 2)
	w.xref[1] = w.offset
	kids := ""
	for _, ref := range w.pageRefs {
		kids += fmt.Sprintf("%d 0 R ", ref)
	}
	pagesObj := fmt.Sprintf("2 0 obj\n<< /Type /Pages /Kids [%s] /Count %d >>\nendobj\n",
		kids, len(w.pageRefs))
	w.writeRaw([]byte(pagesObj))

	// Catalog 오브젝트 (오브젝트 1)
	w.xref[0] = w.offset
	catalogObj := "1 0 obj\n<< /Type /Catalog /Pages 2 0 R >>\nendobj\n"
	w.writeRaw([]byte(catalogObj))

	// xref 테이블
	xrefOffset := w.offset
	totalObjects := len(w.xref) + 1 // +1 for free object 0
	w.writeRaw([]byte(fmt.Sprintf("xref\n0 %d\n", totalObjects)))
	w.writeRaw([]byte(fmt.Sprintf("%010d %05d f \r\n", 0, 65535))) // object 0 (free)
	for _, off := range w.xref {
		w.writeRaw([]byte(fmt.Sprintf("%010d %05d n \r\n", off, 0)))
	}

	// trailer
	trailer := fmt.Sprintf("trailer\n<< /Size %d /Root 1 0 R >>\nstartxref\n%d\n%%%%EOF\n",
		totalObjects, xrefOffset)
	w.writeRaw([]byte(trailer))

	return w.f.Close()
}

func (w *StreamingPDFWriter) writeObjStart(objNum int) {
	// xref 슬라이스 확장
	for len(w.xref) < objNum {
		w.xref = append(w.xref, 0)
	}
	w.xref[objNum-1] = w.offset

	header := fmt.Sprintf("%d 0 obj\n", objNum)
	w.writeRaw([]byte(header))
}

func (w *StreamingPDFWriter) writeRaw(data []byte) {
	n, _ := w.f.Write(data)
	w.offset += int64(n)
}

// ============================================================
// 유틸리티
// ============================================================

type jpegInfo struct {
	Width, Height int
	ColorSpace    string // "DeviceRGB" or "DeviceGray"
}

// readJPEGInfo: JPEG 헤더만 읽어 크기/색공간 파악 (전체 디코딩 없음)
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

	info := jpegInfo{Width: cfg.Width, Height: cfg.Height}
	switch cfg.ColorModel {
	case color.GrayModel:
		info.ColorSpace = "DeviceGray"
	default:
		info.ColorSpace = "DeviceRGB"
	}
	return info, nil
}

func getAlloc() float64 {
	var m runtime.MemStats
	runtime.ReadMemStats(&m)
	return float64(m.Alloc) / (1024 * 1024)
}

// generateTestImages: JPEG 코멘트로 고유성 확보한 테스트 이미지 생성
func generateTestImages(dir string, count int) []string {
	// 그라데이션 템플릿 생성
	img := image.NewNRGBA(image.Rect(0, 0, imgWidth, imgHeight))
	for y := 0; y < imgHeight; y++ {
		for x := 0; x < imgWidth; x++ {
			r := uint8(200 + (x * 55 / imgWidth))
			g := uint8(210 + (y * 45 / imgHeight))
			b := uint8(180)
			img.SetNRGBA(x, y, color.NRGBA{R: r, G: g, B: b, A: 255})
		}
	}

	tplPath := filepath.Join(dir, "_tpl.jpg")
	tf, _ := os.Create(tplPath)
	jpeg.Encode(tf, img, &jpeg.Options{Quality: 85})
	tf.Close()
	img = nil // 메모리 해제

	tplData, _ := os.ReadFile(tplPath)
	os.Remove(tplPath)
	fmt.Printf("  템플릿 JPEG: %.1f KB\n", float64(len(tplData))/1024)

	paths := make([]string, count)
	for i := 0; i < count; i++ {
		comment := fmt.Sprintf("page_%06d_id_%d", i, i*7919+13)
		cb := []byte(comment)
		cLen := len(cb) + 2

		// JPEG 코멘트 마커: FF FE [길이] [데이터]
		seg := []byte{0xFF, 0xFE, byte(cLen >> 8), byte(cLen & 0xFF)}
		seg = append(seg, cb...)

		data := make([]byte, 0, len(tplData)+len(seg))
		data = append(data, tplData[:2]...)
		data = append(data, seg...)
		data = append(data, tplData[2:]...)

		p := filepath.Join(dir, fmt.Sprintf("img_%04d.jpg", i))
		os.WriteFile(p, data, 0644)
		paths[i] = p
	}

	return paths
}
