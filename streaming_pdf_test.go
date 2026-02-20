package main

import (
	"fmt"
	"os"
	"runtime"
	"testing"
)

func TestAddPNGPage(t *testing.T) {
	pngPath := "test_image.png"
	if _, err := os.Stat(pngPath); err != nil {
		t.Skipf("test_image.png 없음: %v", err)
	}

	output := t.TempDir() + "/output.pdf"
	w, err := NewStreamingPDFWriter(output)
	if err != nil {
		t.Fatal(err)
	}

	var m runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&m)
	before := m.Alloc

	for i := 0; i < 10; i++ {
		if err := w.AddPNGPage(pngPath); err != nil {
			t.Fatalf("페이지 %d 추가 실패: %v", i+1, err)
		}
	}

	runtime.GC()
	runtime.ReadMemStats(&m)
	after := m.Alloc

	if err := w.Close(); err != nil {
		t.Fatal(err)
	}

	fi, _ := os.Stat(output)
	fmt.Printf("  PNG 패스스루 10페이지: %.1f KB\n", float64(fi.Size())/1024)
	fmt.Printf("  메모리 증가: %.2f MB\n", float64(after-before)/(1024*1024))

	if w.PageCount() != 10 {
		t.Errorf("페이지 수 = %d, want 10", w.PageCount())
	}
	if fi.Size() < 1000 {
		t.Errorf("PDF 파일이 너무 작음: %d bytes", fi.Size())
	}
}

func TestAddImagePageAutoDetect(t *testing.T) {
	pngPath := "test_image.png"
	if _, err := os.Stat(pngPath); err != nil {
		t.Skipf("test_image.png 없음: %v", err)
	}

	output := t.TempDir() + "/output.pdf"
	w, err := NewStreamingPDFWriter(output)
	if err != nil {
		t.Fatal(err)
	}

	// AddImagePage로 자동 감지
	if err := w.AddImagePage(pngPath); err != nil {
		t.Fatalf("AddImagePage 실패: %v", err)
	}

	if err := w.Close(); err != nil {
		t.Fatal(err)
	}

	fi, _ := os.Stat(output)
	fmt.Printf("  자동감지(PNG): %.1f KB, %d페이지\n", float64(fi.Size())/1024, w.PageCount())
}
