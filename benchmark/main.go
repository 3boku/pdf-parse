package main

import (
	"fmt"
	"image"
	"image/color"
	"image/draw"
	"image/jpeg"
	"os"
	"path/filepath"
	"time"

	"github.com/pdfcpu/pdfcpu/pkg/api"
)

const (
	imgWidth    = 2480
	imgHeight   = 3508
	totalPages  = 1000
	insertCount = 100
	insertAt    = 500 // 500페이지 뒤에 삽입
)

func main() {
	fmt.Println("============================================")
	fmt.Println("  대용량 PDF 벤치마크")
	fmt.Println("============================================")
	fmt.Printf("기존 PDF: %d 페이지\n", totalPages)
	fmt.Printf("삽입할 이미지: %d 장\n", insertCount)
	fmt.Printf("최종 PDF: %d 페이지\n", totalPages+insertCount)
	fmt.Printf("이미지 크기: %dx%d px (A4 300dpi)\n", imgWidth, imgHeight)
	fmt.Printf("삽입 위치: 페이지 %d 뒤\n\n", insertAt)

	tmpDir, err := os.MkdirTemp("", "pdf_bench_*")
	if err != nil {
		fmt.Fprintf(os.Stderr, "임시 디렉토리 생성 실패: %v\n", err)
		os.Exit(1)
	}
	defer os.RemoveAll(tmpDir)

	// ==========================================
	// 셋업: 테스트 데이터 생성
	// ==========================================
	fmt.Println("--- 셋업: 테스트 데이터 생성 ---")
	setupStart := time.Now()

	// 1) 템플릿 이미지 1개 생성 후 JPEG 바이너리 기반으로 고유 이미지 1100개 생성
	step := time.Now()
	imgDir := filepath.Join(tmpDir, "images")
	os.MkdirAll(imgDir, 0755)
	allImages, err := generateUniqueImages(imgDir, totalPages+insertCount)
	if err != nil {
		fmt.Fprintf(os.Stderr, "이미지 생성 실패: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("  고유 이미지 %d개 생성: %v\n", len(allImages), time.Since(step))

	// 이미지 파일 크기 샘플 확인
	fiSample, _ := os.Stat(allImages[0])
	fmt.Printf("  이미지 파일 크기 (샘플): %.1f KB\n", float64(fiSample.Size())/1024)

	// 2) 1000페이지 기본 PDF 생성
	step = time.Now()
	basePDF := filepath.Join(tmpDir, "base_1000.pdf")
	fmt.Printf("  1000페이지 PDF 생성 중...")
	if err := api.ImportImagesFile(allImages[:totalPages], basePDF, nil, nil); err != nil {
		fmt.Fprintf(os.Stderr, "\n  실패: %v\n", err)
		os.Exit(1)
	}
	fi, _ := os.Stat(basePDF)
	fmt.Printf(" 완료! (%.1f MB, %v)\n", float64(fi.Size())/(1024*1024), time.Since(step))

	fmt.Printf("  셋업 총 소요: %v\n\n", time.Since(setupStart))

	// ==========================================
	// Method 1: 1000페이지 PDF에 100장 이미지 삽입
	// ==========================================
	fmt.Println("--- Method 1: 1000페이지 PDF 중간에 100장 삽입 ---")
	fmt.Println("  (PDF 분할 → 100장 이미지PDF 생성 → 3개 PDF 병합)")
	m1Dir := filepath.Join(tmpDir, "m1")
	os.MkdirAll(m1Dir, 0755)
	insertImages := allImages[totalPages:] // 마지막 100개 이미지

	m1Start := time.Now()

	// Step 1: PDF 분할
	step = time.Now()
	firstPDF := filepath.Join(m1Dir, "first_500.pdf")
	secondPDF := filepath.Join(m1Dir, "last_500.pdf")
	if err := api.TrimFile(basePDF, firstPDF, []string{fmt.Sprintf("1-%d", insertAt)}, nil); err != nil {
		fmt.Fprintf(os.Stderr, "  분할 실패 (앞): %v\n", err)
		os.Exit(1)
	}
	if err := api.TrimFile(basePDF, secondPDF, []string{fmt.Sprintf("%d-%d", insertAt+1, totalPages)}, nil); err != nil {
		fmt.Fprintf(os.Stderr, "  분할 실패 (뒤): %v\n", err)
		os.Exit(1)
	}
	fiFirst, _ := os.Stat(firstPDF)
	fiSecond, _ := os.Stat(secondPDF)
	fmt.Printf("  1) PDF 분할 (1-%d, %d-%d): %v\n", insertAt, insertAt+1, totalPages, time.Since(step))
	fmt.Printf("     앞: %.1f MB, 뒤: %.1f MB\n", float64(fiFirst.Size())/(1024*1024), float64(fiSecond.Size())/(1024*1024))

	// Step 2: 100장 이미지로 PDF 생성
	step = time.Now()
	insertPDF := filepath.Join(m1Dir, "insert_100.pdf")
	if err := api.ImportImagesFile(insertImages, insertPDF, nil, nil); err != nil {
		fmt.Fprintf(os.Stderr, "  이미지→PDF 실패: %v\n", err)
		os.Exit(1)
	}
	fiInsert, _ := os.Stat(insertPDF)
	fmt.Printf("  2) 100장 이미지 → PDF: %v (%.1f MB)\n", time.Since(step), float64(fiInsert.Size())/(1024*1024))

	// Step 3: 3개 PDF 병합
	step = time.Now()
	outputPDF1 := filepath.Join(m1Dir, "output_1100.pdf")
	if err := api.MergeCreateFile([]string{firstPDF, insertPDF, secondPDF}, outputPDF1, false, nil); err != nil {
		fmt.Fprintf(os.Stderr, "  병합 실패: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("  3) PDF 3개 병합: %v\n", time.Since(step))

	elapsed1 := time.Since(m1Start)
	fi1, _ := os.Stat(outputPDF1)
	m1Pages, _ := api.PageCountFile(outputPDF1)
	fmt.Printf("  ** Method 1 총 소요: %v (결과: %.1f MB, %d페이지)\n\n", elapsed1, float64(fi1.Size())/(1024*1024), m1Pages)

	// ==========================================
	// Method 2: 1100장 이미지로 PDF 처음부터 생성
	// ==========================================
	fmt.Println("--- Method 2: 1100장 이미지로 PDF 생성 ---")
	fmt.Println("  (1100개 이미지 파일 → 1100페이지 PDF)")
	m2Dir := filepath.Join(tmpDir, "m2")
	os.MkdirAll(m2Dir, 0755)

	// 이미지 순서: 앞 500장 + 삽입 100장 + 뒤 500장
	ordered := make([]string, 0, totalPages+insertCount)
	ordered = append(ordered, allImages[:insertAt]...)
	ordered = append(ordered, insertImages...)
	ordered = append(ordered, allImages[insertAt:totalPages]...)

	m2Start := time.Now()
	outputPDF2 := filepath.Join(m2Dir, "output_1100.pdf")
	fmt.Printf("  1100장 이미지 → PDF 생성 중...")
	if err := api.ImportImagesFile(ordered, outputPDF2, nil, nil); err != nil {
		fmt.Fprintf(os.Stderr, "\n  실패: %v\n", err)
		os.Exit(1)
	}
	elapsed2 := time.Since(m2Start)
	fi2, _ := os.Stat(outputPDF2)
	m2Pages, _ := api.PageCountFile(outputPDF2)
	fmt.Printf(" 완료!\n")
	fmt.Printf("  ** Method 2 총 소요: %v (결과: %.1f MB, %d페이지)\n\n", elapsed2, float64(fi2.Size())/(1024*1024), m2Pages)

	// ==========================================
	// 결과 비교
	// ==========================================
	fmt.Println("============================================")
	fmt.Println("  최종 결과")
	fmt.Println("============================================")
	fmt.Printf("Method 1 (PDF에 삽입):       %v\n", elapsed1)
	fmt.Printf("  - PDF 분할 (2회):          포함\n")
	fmt.Printf("  - 100장 이미지→PDF:        포함\n")
	fmt.Printf("  - 3개 PDF 병합:            포함\n")
	fmt.Printf("  - 결과 파일: %.1f MB\n", float64(fi1.Size())/(1024*1024))
	fmt.Println()
	fmt.Printf("Method 2 (이미지→PDF):       %v\n", elapsed2)
	fmt.Printf("  - 1100장 이미지→PDF:       전부\n")
	fmt.Printf("  - 결과 파일: %.1f MB\n", float64(fi2.Size())/(1024*1024))
	fmt.Println()

	if elapsed1 < elapsed2 {
		fmt.Printf(">> Method 1이 %.2fx 빠릅니다\n", float64(elapsed2)/float64(elapsed1))
	} else if elapsed2 < elapsed1 {
		fmt.Printf(">> Method 2가 %.2fx 빠릅니다\n", float64(elapsed1)/float64(elapsed2))
	} else {
		fmt.Println(">> 두 방법의 속도가 동일합니다")
	}
}

// generateUniqueImages: 템플릿 JPEG 1개를 만든 뒤, JPEG 코멘트 마커를 이용해
// 바이너리가 고유한 이미지 파일 N개를 빠르게 생성
func generateUniqueImages(outDir string, count int) ([]string, error) {
	// 템플릿 이미지 생성 (2480x3508 JPEG)
	img := image.NewNRGBA(image.Rect(0, 0, imgWidth, imgHeight))

	// 그라데이션 패턴으로 채워서 좀 더 현실적인 파일 크기 확보
	for y := 0; y < imgHeight; y++ {
		for x := 0; x < imgWidth; x++ {
			r := uint8(200 + (x*55/imgWidth))
			g := uint8(210 + (y*45/imgHeight))
			b := uint8(180 + ((x+y)*30/(imgWidth+imgHeight)))
			img.SetNRGBA(x, y, color.NRGBA{R: r, G: g, B: b, A: 255})
		}
	}

	// 템플릿을 메모리에 JPEG 인코딩
	templatePath := filepath.Join(outDir, "_template.jpg")
	tf, err := os.Create(templatePath)
	if err != nil {
		return nil, err
	}
	if err := jpeg.Encode(tf, img, &jpeg.Options{Quality: 85}); err != nil {
		tf.Close()
		return nil, err
	}
	tf.Close()

	templateData, err := os.ReadFile(templatePath)
	if err != nil {
		return nil, err
	}
	os.Remove(templatePath)

	fmt.Printf("  템플릿 JPEG 크기: %.1f KB\n", float64(len(templateData))/1024)

	// 각 이미지에 고유한 JPEG 코멘트(0xFFFE) 삽입하여 바이너리 고유성 확보
	paths := make([]string, count)
	for i := 0; i < count; i++ {
		comment := fmt.Sprintf("page_%06d_unique_identifier_%d", i, i*7919+13) // 고유 문자열
		commentBytes := []byte(comment)
		commentLen := len(commentBytes) + 2 // JPEG 코멘트 길이 필드는 자기 자신 2바이트 포함

		// JPEG 코멘트 세그먼트: FF FE [길이 2바이트] [코멘트]
		segment := make([]byte, 0, 4+len(commentBytes))
		segment = append(segment, 0xFF, 0xFE, byte(commentLen>>8), byte(commentLen&0xFF))
		segment = append(segment, commentBytes...)

		// SOI(FF D8) 뒤에 코멘트 삽입
		data := make([]byte, 0, len(templateData)+len(segment))
		data = append(data, templateData[:2]...) // SOI
		data = append(data, segment...)           // 코멘트
		data = append(data, templateData[2:]...) // 나머지

		p := filepath.Join(outDir, fmt.Sprintf("img_%04d.jpg", i))
		if err := os.WriteFile(p, data, 0644); err != nil {
			return nil, err
		}
		paths[i] = p

		if (i+1)%200 == 0 {
			fmt.Printf("  이미지 생성 진행: %d/%d\n", i+1, count)
		}
	}

	return paths, nil
}

// generateImage: 단색 JPEG 이미지 생성 (더 이상 사용하지 않지만 참고용)
func generateImage(path string, r, g, b uint8) {
	img := image.NewNRGBA(image.Rect(0, 0, imgWidth, imgHeight))
	fill := &image.Uniform{color.NRGBA{R: r, G: g, B: b, A: 255}}
	draw.Draw(img, img.Bounds(), fill, image.Point{}, draw.Src)
	f, _ := os.Create(path)
	defer f.Close()
	jpeg.Encode(f, img, &jpeg.Options{Quality: 85})
}
