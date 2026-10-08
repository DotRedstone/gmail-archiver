package main

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"

	"github.com/dot/gmail-archiver/internal/api"
	"github.com/dot/gmail-archiver/internal/config"
	"github.com/dot/gmail-archiver/internal/db"
	"github.com/dot/gmail-archiver/internal/parser"
	"github.com/dot/gmail-archiver/internal/rule"
	"github.com/dot/gmail-archiver/internal/storage"
)

func main() {
	fmt.Println("=== 开始本地端到端收作业业务流仿真测试 ===")

	// 1. 初始化临时测试环境
	tmpDataDir, err := os.MkdirTemp("", "gmail_archiver_e2e_*")
	if err != nil {
		log.Fatalf("创建临时目录失败: %v", err)
	}
	defer os.RemoveAll(tmpDataDir)

	// 复制名单和规则到测试目录
	rosterDir := filepath.Join(tmpDataDir, "rosters")
	rulesDir := filepath.Join(tmpDataDir, "rules")
	_ = os.MkdirAll(rosterDir, 0o755)
	_ = os.MkdirAll(rulesDir, 0o755)

	copyFile("data/rosters/2024_cs_5.csv", filepath.Join(rosterDir, "2024_cs_5.csv"))
	copyFile("data/rosters/2024_green_compute_1.csv", filepath.Join(rosterDir, "2024_green_compute_1.csv"))
	copyFile("data/rules/parallel_computing_lab1.yaml", filepath.Join(rulesDir, "parallel_computing_lab1.yaml"))

	cfg := &config.Config{
		DataDir:  tmpDataDir,
		RulesDir: rulesDir,
		HTTPPort: 8080,
	}

	storageEngine, err := storage.New(cfg.DataDir)
	if err != nil {
		log.Fatalf("初始化存储失败: %v", err)
	}

	database, err := db.Open(cfg.DataDir)
	if err != nil {
		log.Fatalf("初始化数据库失败: %v", err)
	}
	defer database.Close()

	emailParser := parser.New(storageEngine)

	ruleEngine := rule.NewEngine(cfg.DataDir)
	ruleObj, err := ruleEngine.LoadRuleFile(filepath.Join(rulesDir, "parallel_computing_lab1.yaml"))
	if err != nil {
		log.Fatalf("加载作业规则失败: %v", err)
	}
	fmt.Printf("✓ 成功加载作业规则: %s (总应交人数: %d人)\n\n", ruleObj.Name, ruleObj.Roster.Count())

	apiServer := api.NewServer(cfg, database, storageEngine, nil, ruleEngine)
	router := apiServer.Routes()

	// 2. 模拟学生 1 (支全振) 首次提交 (v1)
	fmt.Println(">> 模拟学生 1 (支全振 240809010501) 首次提交作业...")
	email1 := `From: "支全振" <zhi@example.com>
Subject: 并行计算-实验1-支全振
Date: Sun, 20 Sep 2026 10:00:00 +0800
Message-ID: <msg-001@example.com>
Content-Type: multipart/mixed; boundary="sep"

--sep
Content-Type: text/plain; charset=utf-8

姓名：支全振
学号：240809010501
班级：241
提交内容：实验1源码及实验报告

--sep
Content-Type: application/zip
Content-Disposition: attachment; filename="实验1-241-240809010501-支全振.zip"
Content-Transfer-Encoding: base64

dmVyc2lvbi0xLWNvbnRlbnQ=
--sep--
`
	simulateIncomingEmail(email1, emailParser, ruleEngine, database)

	// 3. 模拟学生 2 (乔可傲 240809012103) 容错提交 (附件文件名漏写学号，正文中补充)
	fmt.Println("\n>> 模拟学生 2 (乔可傲 240809012103) 提交作业 (附件名漏写学号，正文容错)...")
	email2 := `From: "乔可傲" <qiao@example.com>
Subject: 并行计算-实验1-乔可傲
Date: Sun, 20 Sep 2026 11:00:00 +0800
Message-ID: <msg-002@example.com>
Content-Type: multipart/mixed; boundary="sep"

--sep
Content-Type: text/plain; charset=utf-8

姓名：乔可傲
学号：240809012103
班级：绿色算力1班
提交内容：实验1源码及实验报告

--sep
Content-Type: application/zip
Content-Disposition: attachment; filename="乔可傲_实验1.zip"
Content-Transfer-Encoding: base64

cWlhby1jb250ZW50
--sep--
`
	simulateIncomingEmail(email2, emailParser, ruleEngine, database)

	// 4. 模拟学生 1 (支全振) 发现代码有 bug，发送更正邮件 (v2 覆盖更新)
	fmt.Println("\n>> 模拟学生 1 (支全振) 再次发送更正邮件 (v2)...")
	email1v2 := `From: "支全振" <zhi@example.com>
Subject: 并行计算-实验1-支全振（更正版）
Date: Sun, 20 Sep 2026 15:30:00 +0800
Message-ID: <msg-003@example.com>
Content-Type: multipart/mixed; boundary="sep"

--sep
Content-Type: text/plain; charset=utf-8

老师好，前面那版代码有bug，以此更正版为准！
姓名：支全振
学号：240809010501
班级：241

--sep
Content-Type: application/zip
Content-Disposition: attachment; filename="实验1-241-240809010501-支全振_更正.zip"
Content-Transfer-Encoding: base64

dmVyc2lvbi0yLXVwZGF0ZWQtY29udGVudA==
--sep--
`
	simulateIncomingEmail(email1v2, emailParser, ruleEngine, database)

	// 5. 调用 REST API 检验统计结果
	fmt.Println("\n>> 正在调用 GET /api/assignments/parallel_computing_lab1/status ...")
	w := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/api/assignments/parallel_computing_lab1/status", nil)
	router.ServeHTTP(w, req)

	var statusMap map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &statusMap)
	fmt.Printf("   应交人数: %v | 实交人数: %v | 未交人数: %v | 提交率: %v\n",
		statusMap["total_expected"], statusMap["submitted_count"], statusMap["missing_count"], statusMap["submission_rate"])

	// 6. 调用未交名单 API (催交名单)
	fmt.Println("\n>> 正在调用 GET /api/assignments/parallel_computing_lab1/missing (催交名单) ...")
	wMissing := httptest.NewRecorder()
	reqMissing := httptest.NewRequest("GET", "/api/assignments/parallel_computing_lab1/missing", nil)
	router.ServeHTTP(wMissing, reqMissing)
	var missingResp struct {
		MissingCount int `json:"missing_count"`
		MissingList  []struct {
			StudentID string `json:"student_id"`
			Name      string `json:"name"`
			ClassName string `json:"class_name"`
		} `json:"missing_list"`
	}
	_ = json.Unmarshal(wMissing.Body.Bytes(), &missingResp)
	fmt.Printf("   当前未交共 %d 人，前 3 名未交学生预览:\n", missingResp.MissingCount)
	for i := 0; i < 3 && i < len(missingResp.MissingList); i++ {
		st := missingResp.MissingList[i]
		fmt.Printf("   - [%s] %s (%s)\n", st.StudentID, st.Name, st.ClassName)
	}

	// 7. 查询支全振的更正历史 (v1 vs v2)
	fmt.Println("\n>> 正在调用 GET /api/assignments/parallel_computing_lab1/submissions/240809010501/history (更正时间线) ...")
	wHist := httptest.NewRecorder()
	reqHist := httptest.NewRequest("GET", "/api/assignments/parallel_computing_lab1/submissions/240809010501/history", nil)
	router.ServeHTTP(wHist, reqHist)
	var histResp struct {
		TotalVersion int `json:"total_version"`
		History      []struct {
			Version        int    `json:"version"`
			IsLatest       bool   `json:"is_latest"`
			SubmittedAt    string `json:"submitted_at"`
			TargetFilename string `json:"target_filename"`
		} `json:"history"`
	}
	_ = json.Unmarshal(wHist.Body.Bytes(), &histResp)
	fmt.Printf("   该生共提交 %d 次版本:\n", histResp.TotalVersion)
	for _, h := range histResp.History {
		statusStr := "历史废弃版本"
		if h.IsLatest {
			statusStr = "★ 当前最终有效版本"
		}
		fmt.Printf("   - 版本 v%d: 提交时间 %s | 命名: %s [%s]\n", h.Version, h.SubmittedAt, h.TargetFilename, statusStr)
	}

	// 8. 模拟一键打包下载 Zip
	fmt.Println("\n>> 正在调用 GET /api/assignments/parallel_computing_lab1/export (一键打包下载 Zip) ...")
	wExport := httptest.NewRecorder()
	reqExport := httptest.NewRequest("GET", "/api/assignments/parallel_computing_lab1/export", nil)
	router.ServeHTTP(wExport, reqExport)

	zipBytes := wExport.Body.Bytes()
	zr, err := zip.NewReader(bytes.NewReader(zipBytes), int64(len(zipBytes)))
	if err != nil {
		log.Fatalf("读取导出的 Zip 失败: %v", err)
	}

	fmt.Printf("   ✓ Zip 打包成功，压缩包大小: %d 字节，包内文件列表:\n", len(zipBytes))
	for _, f := range zr.File {
		rc, _ := f.Open()
		content, _ := io.ReadAll(rc)
		rc.Close()
		fmt.Printf("   - 归档文件: %s (内容: %q)\n", f.Name, string(content))
	}

	fmt.Println("\n=== 全部端到端业务流仿真测试成功通过！ ===")
}

func simulateIncomingEmail(rawEmail string, p *parser.Parser, r *rule.Engine, d *db.DB) {
	meta, err := p.Parse(strings.NewReader(rawEmail))
	if err != nil {
		log.Fatalf("解析邮件失败: %v", err)
	}

	for _, att := range meta.Attachments {
		attID, err := d.InsertAttachment(att)
		if err != nil {
			log.Fatalf("附件入库失败: %v", err)
		}

		if _, matchRes, ok := r.Match(meta.Subject, att.Filename, meta.BodyText, meta.ReceivedAt); ok {
			sub := &db.Submission{
				AssignmentID:   matchRes.AssignmentID,
				StudentID:      matchRes.StudentID,
				StudentName:    matchRes.StudentName,
				ClassName:      matchRes.ClassName,
				AttachmentID:   attID,
				SubmittedAt:    meta.ReceivedAt,
				IsLate:         matchRes.IsLate,
				TargetFilename: matchRes.TargetFilename,
			}
			ver, isUpdate, err := d.RecordSubmission(sub)
			if err != nil {
				log.Fatalf("作业入库失败: %v", err)
			}
			fmt.Printf("   [匹配成功] 学生: %s (%s) | 作业: %s | 版本: v%d | 更正更新: %v | 迟交: %v\n",
				matchRes.StudentName, matchRes.StudentID, matchRes.AssignmentID, ver, isUpdate, matchRes.IsLate)
		} else {
			fmt.Println("   [未匹配作业]")
		}
	}
}

func copyFile(src, dst string) {
	in, err := os.Open(src)
	if err != nil {
		log.Fatalf("打开源文件失败: %v", err)
	}
	defer in.Close()

	out, err := os.Create(dst)
	if err != nil {
		log.Fatalf("创建目标文件失败: %v", err)
	}
	defer out.Close()

	_, err = io.Copy(out, in)
	if err != nil {
		log.Fatalf("复制文件失败: %v", err)
	}
}
