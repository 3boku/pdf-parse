package main

import (
	"fmt"
	"image"
	"image/png"
	"os"
	"path/filepath"
	"strings"

	"github.com/gen2brain/go-fitz"
	"golang.org/x/image/draw"
)

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintf(os.Stderr, "Usage: %s <input.pdf> [output_dir]\n", os.Args[0])
		os.Exit(1)
	}

	pdfPath := os.Args[1]
	outputDir := "."
	if len(os.Args) >= 3 {
		outputDir = os.Args[2]
	}

	doc, err := fitz.New(pdfPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "PDF 열기 실패: %v\n", err)
		os.Exit(1)
	}
	defer doc.Close()

	baseName := strings.TrimSuffix(filepath.Base(pdfPath), filepath.Ext(pdfPath))

	if err := os.MkdirAll(outputDir, 0755); err != nil {
		fmt.Fprintf(os.Stderr, "출력 디렉토리 생성 실패: %v\n", err)
		os.Exit(1)
	}

	const targetHeight = 1200

	for i := 0; i < doc.NumPage(); i++ {
		img, err := doc.Image(i)
		if err != nil {
			fmt.Fprintf(os.Stderr, "페이지 %d 렌더링 실패: %v\n", i+1, err)
			continue
		}

		resized := resizeToHeight(img, targetHeight)

		outPath := filepath.Join(outputDir, fmt.Sprintf("%s_page_%03d.png", baseName, i+1))
		f, err := os.Create(outPath)
		if err != nil {
			fmt.Fprintf(os.Stderr, "파일 생성 실패 %s: %v\n", outPath, err)
			continue
		}

		if err := png.Encode(f, resized); err != nil {
			f.Close()
			fmt.Fprintf(os.Stderr, "페이지 %d PNG 인코딩 실패: %v\n", i+1, err)
			continue
		}
		f.Close()

		fmt.Printf("페이지 %d -> %s (%dx%d)\n", i+1, outPath, resized.Bounds().Dx(), resized.Bounds().Dy())
	}

	fmt.Printf("완료: 총 %d 페이지 처리\n", doc.NumPage())
}

func resizeToHeight(src image.Image, targetHeight int) image.Image {
	srcBounds := src.Bounds()
	srcW := srcBounds.Dx()
	srcH := srcBounds.Dy()

	if srcH == targetHeight {
		return src
	}

	ratio := float64(targetHeight) / float64(srcH)
	newW := int(float64(srcW) * ratio)

	dst := image.NewRGBA(image.Rect(0, 0, newW, targetHeight))
	draw.CatmullRom.Scale(dst, dst.Bounds(), src, srcBounds, draw.Over, nil)

	return dst
}
