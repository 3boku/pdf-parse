package main

import (
	"fmt"
	"image/png"
	"os"
	"path/filepath"
	"time"

	"github.com/gen2brain/go-fitz"
	"github.com/pdfcpu/pdfcpu/pkg/api"
)

const iterations = 5

func main() {
	pdfPath := "input.pdf"
	testImagePath := "test_image.png"

	if _, err := os.Stat(pdfPath); err != nil {
		fmt.Fprintf(os.Stderr, "PDF 파일을 찾을 수 없습니다: %v\n", err)
		os.Exit(1)
	}
	if _, err := os.Stat(testImagePath); err != nil {
		fmt.Fprintf(os.Stderr, "테스트 이미지를 찾을 수 없습니다: %v\n", err)
		os.Exit(1)
	}

	// PDF 페이지 수 확인
	doc, err := fitz.New(pdfPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "PDF 열기 실패: %v\n", err)
		os.Exit(1)
	}
	pageCount := doc.NumPage()
	doc.Close()

	fmt.Println("=== PDF 이미지 삽입 벤치마크 ===")
	fmt.Printf("PDF: %s (%d 페이지)\n", pdfPath, pageCount)
	fmt.Printf("삽입할 이미지: %s\n", testImagePath)
	fmt.Printf("삽입 위치: 페이지 1과 2 사이\n")
	fmt.Printf("반복 횟수: %d\n\n", iterations)

	// === Method 1: PDF 직접 조작 ===
	fmt.Println("--- Method 1: PDF 직접 조작 (분할 → 이미지PDF 생성 → 병합) ---")
	var total1 time.Duration
	for i := 0; i < iterations; i++ {
		tmpDir, _ := os.MkdirTemp("", "bench_m1_*")
		start := time.Now()
		err := method1DirectInsert(pdfPath, testImagePath, tmpDir)
		elapsed := time.Since(start)
		os.RemoveAll(tmpDir)
		if err != nil {
			fmt.Fprintf(os.Stderr, "  Method 1 실패: %v\n", err)
			break
		}
		total1 += elapsed
		fmt.Printf("  Run %d: %v\n", i+1, elapsed)
	}

	fmt.Println()

	// === Method 2: PNG 라운드트립 ===
	fmt.Println("--- Method 2: PNG 라운드트립 (PDF→PNG 추출 → 이미지 삽입 → PNG→PDF) ---")
	var total2 time.Duration
	for i := 0; i < iterations; i++ {
		tmpDir, _ := os.MkdirTemp("", "bench_m2_*")
		start := time.Now()
		err := method2PNGRoundtrip(pdfPath, testImagePath, tmpDir)
		elapsed := time.Since(start)
		os.RemoveAll(tmpDir)
		if err != nil {
			fmt.Fprintf(os.Stderr, "  Method 2 실패: %v\n", err)
			break
		}
		total2 += elapsed
		fmt.Printf("  Run %d: %v\n", i+1, elapsed)
	}

	// === 결과 비교 ===
	fmt.Println()
	fmt.Println("=== 결과 비교 ===")
	avg1 := total1 / time.Duration(iterations)
	avg2 := total2 / time.Duration(iterations)
	fmt.Printf("Method 1 (PDF 직접 조작):  평균 %v (총 %v)\n", avg1, total1)
	fmt.Printf("Method 2 (PNG 라운드트립): 평균 %v (총 %v)\n", avg2, total2)
	fmt.Println()

	if avg1 < avg2 {
		speedup := float64(avg2) / float64(avg1)
		fmt.Printf(">> Method 1이 %.2fx 빠릅니다\n", speedup)
	} else if avg2 < avg1 {
		speedup := float64(avg1) / float64(avg2)
		fmt.Printf(">> Method 2가 %.2fx 빠릅니다\n", speedup)
	} else {
		fmt.Println(">> 두 방법의 속도가 동일합니다")
	}

	fmt.Println()
	fmt.Println("참고: Method 1은 원본 PDF 벡터 품질을 유지하고, Method 2는 모든 페이지가 래스터 이미지로 변환됩니다.")
}

// Method 1: PDF를 직접 조작하여 이미지를 페이지로 삽입
// 1. PDF에서 페이지 1 추출
// 2. PDF에서 나머지 페이지(2~) 추출
// 3. 테스트 이미지로 단일 페이지 PDF 생성
// 4. 세 PDF를 병합: [페이지1] + [이미지] + [나머지]
func method1DirectInsert(pdfPath, imagePath, tmpDir string) error {
	page1PDF := filepath.Join(tmpDir, "page1.pdf")
	restPDF := filepath.Join(tmpDir, "rest.pdf")
	imagePDF := filepath.Join(tmpDir, "image.pdf")
	outputPDF := filepath.Join(tmpDir, "output.pdf")

	// 페이지 1 추출
	if err := api.TrimFile(pdfPath, page1PDF, []string{"1"}, nil); err != nil {
		return fmt.Errorf("페이지 1 추출 실패: %w", err)
	}

	// 나머지 페이지 추출
	if err := api.TrimFile(pdfPath, restPDF, []string{"2-"}, nil); err != nil {
		return fmt.Errorf("나머지 페이지 추출 실패: %w", err)
	}

	// 이미지를 PDF로 변환
	if err := api.ImportImagesFile([]string{imagePath}, imagePDF, nil, nil); err != nil {
		return fmt.Errorf("이미지 PDF 변환 실패: %w", err)
	}

	// 병합: page1 + image + rest
	if err := api.MergeCreateFile([]string{page1PDF, imagePDF, restPDF}, outputPDF, false, nil); err != nil {
		return fmt.Errorf("PDF 병합 실패: %w", err)
	}

	return nil
}

// Method 2: PDF를 PNG로 추출 → 테스트 이미지를 사이에 삽입 → PNG들로 PDF 재생성
// 1. go-fitz로 모든 페이지를 PNG로 추출
// 2. 페이지 1 PNG 뒤에 테스트 이미지 삽입
// 3. 전체 이미지 목록으로 새 PDF 생성
func method2PNGRoundtrip(pdfPath, imagePath, tmpDir string) error {
	// PDF의 모든 페이지를 PNG로 추출
	doc, err := fitz.New(pdfPath)
	if err != nil {
		return fmt.Errorf("PDF 열기 실패: %w", err)
	}
	defer doc.Close()

	var allImages []string

	for i := 0; i < doc.NumPage(); i++ {
		img, err := doc.Image(i)
		if err != nil {
			return fmt.Errorf("페이지 %d 렌더링 실패: %w", i+1, err)
		}

		outPath := filepath.Join(tmpDir, fmt.Sprintf("page_%03d.png", i+1))
		f, err := os.Create(outPath)
		if err != nil {
			return fmt.Errorf("파일 생성 실패: %w", err)
		}

		if err := png.Encode(f, img); err != nil {
			f.Close()
			return fmt.Errorf("PNG 인코딩 실패: %w", err)
		}
		f.Close()

		// 페이지 1 뒤에 테스트 이미지 삽입
		if i == 0 {
			allImages = append(allImages, outPath)
			allImages = append(allImages, imagePath)
		} else {
			allImages = append(allImages, outPath)
		}
	}

	// 모든 이미지로 PDF 생성
	outputPDF := filepath.Join(tmpDir, "output.pdf")
	if err := api.ImportImagesFile(allImages, outputPDF, nil, nil); err != nil {
		return fmt.Errorf("PDF 생성 실패: %w", err)
	}

	return nil
}
