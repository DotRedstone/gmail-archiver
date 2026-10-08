package api

import (
	"net/http"
)

// [Docs]
func (s *Server) handleDocs(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(htmlDocsContent))
}

const htmlDocsContent = `<!DOCTYPE html>
<html lang="zh-CN">
<head>
  <meta charset="UTF-8">
  <meta name="viewport" content="width=device-width, initial-scale=1.0">
  <title>gmail-archiver API 接口文档与导出控制台</title>
  <style>
    :root {
      --bg: #0f172a;
      --card-bg: #1e293b;
      --border: #334155;
      --text: #f8fafc;
      --text-muted: #94a3b8;
      --accent: #38bdf8;
      --accent-hover: #0284c7;
      --get-badge: #0284c7;
      --code-bg: #090d16;
      --success: #10b981;
      --warning: #f59e0b;
    }
    * { box-sizing: border-box; margin: 0; padding: 0; }
    body {
      font-family: -apple-system, BlinkMacSystemFont, "Segoe UI", Roboto, "Helvetica Neue", Arial, sans-serif;
      background-color: var(--bg);
      color: var(--text);
      line-height: 1.6;
      padding: 24px 16px 80px;
    }
    .container { max-width: 1040px; margin: 0 auto; }
    header {
      border-bottom: 1px solid var(--border);
      padding-bottom: 20px;
      margin-bottom: 24px;
      display: flex;
      flex-wrap: wrap;
      justify-content: space-between;
      align-items: center;
      gap: 16px;
    }
    h1 { font-size: 1.75rem; font-weight: 700; color: #fff; display: flex; align-items: center; gap: 10px; }
    .badge {
      font-size: 0.75rem;
      padding: 3px 8px;
      border-radius: 9999px;
      background: rgba(56, 189, 248, 0.15);
      color: var(--accent);
      border: 1px solid rgba(56, 189, 248, 0.3);
    }
    .lead { color: var(--text-muted); font-size: 0.95rem; margin-top: 6px; }

    /* Token bar */
    .token-bar {
      background: linear-gradient(135deg, rgba(30, 41, 59, 0.9), rgba(15, 23, 42, 0.9));
      border: 1px solid var(--accent);
      border-radius: 12px;
      padding: 16px 20px;
      margin-bottom: 28px;
      display: flex;
      flex-wrap: wrap;
      align-items: center;
      gap: 14px;
      box-shadow: 0 4px 20px rgba(56, 189, 248, 0.1);
    }
    .token-label { font-weight: 600; font-size: 0.95rem; color: #fff; display: flex; align-items: center; gap: 6px; }
    .token-input {
      flex: 1;
      min-width: 220px;
      background: var(--code-bg);
      border: 1px solid var(--border);
      color: #fff;
      padding: 8px 14px;
      border-radius: 8px;
      font-family: monospace;
      font-size: 0.9rem;
      outline: none;
      transition: border-color 0.2s;
    }
    .token-input:focus { border-color: var(--accent); }
    .token-hint { font-size: 0.8rem; color: var(--text-muted); width: 100%; margin-top: -4px; }

    /* Section & Cards */
    .section-title {
      font-size: 1.25rem;
      font-weight: 600;
      color: #fff;
      margin: 32px 0 16px;
      display: flex;
      align-items: center;
      gap: 8px;
      border-left: 4px solid var(--accent);
      padding-left: 10px;
    }
    .card {
      background: var(--card-bg);
      border: 1px solid var(--border);
      border-radius: 12px;
      padding: 18px 20px;
      margin-bottom: 16px;
      transition: border-color 0.2s;
    }
    .card:hover { border-color: rgba(56, 189, 248, 0.4); }
    .endpoint-header {
      display: flex;
      flex-wrap: wrap;
      align-items: center;
      gap: 12px;
      margin-bottom: 10px;
    }
    .method-badge {
      background: var(--get-badge);
      color: #fff;
      font-size: 0.75rem;
      font-weight: 700;
      padding: 3px 8px;
      border-radius: 6px;
      text-transform: uppercase;
    }
    .endpoint-path {
      font-family: monospace;
      font-size: 1.05rem;
      font-weight: 600;
      color: #fff;
    }
    .endpoint-desc { font-size: 0.9rem; color: var(--text-muted); margin-bottom: 12px; }
    .btn-download {
      display: inline-flex;
      align-items: center;
      gap: 6px;
      background: var(--accent);
      color: #0f172a;
      font-weight: 600;
      font-size: 0.85rem;
      padding: 6px 14px;
      border-radius: 6px;
      text-decoration: none;
      margin-top: 6px;
      transition: background 0.2s, transform 0.1s;
    }
    .btn-download:hover { background: var(--accent-hover); color: #fff; transform: translateY(-1px); }

    /* Tables & Code */
    table { width: 100%; border-collapse: collapse; margin: 12px 0; font-size: 0.85rem; }
    th, td { text-align: left; padding: 8px 12px; border-bottom: 1px solid rgba(255,255,255,0.06); }
    th { color: var(--text-muted); font-weight: 600; background: rgba(0,0,0,0.15); }
    td code { background: rgba(255,255,255,0.08); padding: 2px 6px; border-radius: 4px; font-size: 0.8rem; }
    pre {
      background: var(--code-bg);
      border: 1px solid var(--border);
      border-radius: 8px;
      padding: 12px 14px;
      overflow-x: auto;
      font-family: "JetBrains Mono", Consolas, Menlo, monospace;
      font-size: 0.82rem;
      color: #e2e8f0;
      margin-top: 10px;
    }
  </style>
</head>
<body>
<div class="container">
  <header>
    <div>
      <h1><span>📬 gmail-archiver</span> <span class="badge">v0.1.0 · REST API 文档</span></h1>
      <p class="lead">高校课程作业多维自动归档、实时催收与批改下载服务</p>
    </div>
  </header>

  <!-- Token 动态调试栏 -->
  <div class="token-bar">
    <div class="token-label">🔑 访问令牌 (Token / API Key)：</div>
    <input type="text" id="tokenInput" class="token-input" placeholder="若启用了 API_KEY，请在此输入 Token..." oninput="updateTokenUrls()">
    <div class="token-hint">💡 提示：在此输入 Token 后，页面内所有一键下载按钮与 cURL 示例均会自动实时拼接参数！</div>
  </div>

  <!-- 场景专区: 多维作业下载与打包 -->
  <div class="section-title">📦 作业下载与导出 API（支持 4 大维度）</div>

  <!-- 维度 1: 单次作业全员打包 -->
  <div class="card">
    <div class="endpoint-header">
      <span class="method-badge">GET</span>
      <span class="endpoint-path">/api/assignments/{id}/export</span>
    </div>
    <div class="endpoint-desc">
      <strong>【场景 1：单次作业全员打包】</strong> 一次性下载某一次作业（如实验1）的全班学生最新有效作业，自动规范重命名并打包为一个 Zip。
    </div>
    <a href="/api/assignments/parallel_computing_lab1/export" class="btn-download dynamic-link" target="_blank">
      ⬇️ 点击一键下载实验1全员作业 (Zip)
    </a>
    <pre class="curl-sample">curl -s "http://localhost:8080/api/assignments/parallel_computing_lab1/export" -o 并行计算实验1_全员作业.zip</pre>
  </div>

  <!-- 维度 2: 单次作业指定学生下载 -->
  <div class="card">
    <div class="endpoint-header">
      <span class="method-badge">GET</span>
      <span class="endpoint-path">/api/assignments/{id}/submissions/{student_id}/download</span>
    </div>
    <div class="endpoint-desc">
      <strong>【场景 2：单次作业单人直接下载】</strong> 下载指定学生在某次作业中提交的最新有效文件（直接返回学生原始压缩包，附规范化重命名）。
    </div>
    <a href="/api/assignments/parallel_computing_lab1/submissions/240809010501/download" class="btn-download dynamic-link" target="_blank">
      ⬇️ 示例：下载支全振 (240809010501) 实验1作业
    </a>
    <pre class="curl-sample">curl -s "http://localhost:8080/api/assignments/parallel_computing_lab1/submissions/240809010501/download" -O</pre>
  </div>

  <!-- 维度 3: 整学期全作业全员打包 -->
  <div class="card">
    <div class="endpoint-header">
      <span class="method-badge">GET</span>
      <span class="endpoint-path">/api/assignments/export/all</span>
    </div>
    <div class="endpoint-desc">
      <strong>【场景 3：整学期全部作业总打包】</strong> 一键下载截止目前所有已加载作业（实验1、实验2、实验3...）的全部学生有效文件，按作业目录自动分类打包为一个 Zip。
    </div>
    <a href="/api/assignments/export/all" class="btn-download dynamic-link" target="_blank">
      ⬇️ 点击一键打包整学期全部作业 (Zip)
    </a>
    <pre class="curl-sample">curl -s "http://localhost:8080/api/assignments/export/all" -o 整学期全量作业归档.zip</pre>
  </div>

  <!-- 维度 4: 纵向下载单人全学期所有作业 -->
  <div class="card">
    <div class="endpoint-header">
      <span class="method-badge">GET</span>
      <span class="endpoint-path">/api/students/{student_id}/export</span>
    </div>
    <div class="endpoint-desc">
      <strong>【场景 4：单人全学期作业纵向总打包】</strong> 纵向提取某位学生截止目前提交的全部课程作业（实验1、实验2...），打包为 <code>{学号}_{姓名}_全部作业.zip</code>（期末复查平时分神器）。
    </div>
    <a href="/api/students/240809010501/export" class="btn-download dynamic-link" target="_blank">
      ⬇️ 示例：下载支全振全学期全部实验汇总 (Zip)
    </a>
    <pre class="curl-sample">curl -s "http://localhost:8080/api/students/240809010501/export" -o 240809010501_支全振_全部作业.zip</pre>
  </div>

  <!-- 统计与催收专区 -->
  <div class="section-title">📊 作业进度查询与催收 API</div>

  <div class="card">
    <div class="endpoint-header">
      <span class="method-badge">GET</span>
      <span class="endpoint-path">/api/assignments/{id}/status</span>
    </div>
    <div class="endpoint-desc">获取指定作业的实时提交进度（应交人数、实交人数、迟交人数、提交率）。</div>
    <pre class="curl-sample">curl -s "http://localhost:8080/api/assignments/parallel_computing_lab1/status" | jq .</pre>
  </div>

  <div class="card">
    <div class="endpoint-header">
      <span class="method-badge">GET</span>
      <span class="endpoint-path">/api/assignments/{id}/missing</span>
    </div>
    <div class="endpoint-desc">获取指定作业的未交名单（直接列出学号、姓名、班级，方便群内通报催交）。</div>
    <pre class="curl-sample">curl -s "http://localhost:8080/api/assignments/parallel_computing_lab1/missing" | jq .</pre>
  </div>

  <div class="card">
    <div class="endpoint-header">
      <span class="method-badge">GET</span>
      <span class="endpoint-path">/api/assignments/{id}/submissions</span>
    </div>
    <div class="endpoint-desc">获取指定作业全部学生的最新有效提交列表与文件哈希元数据。</div>
    <pre class="curl-sample">curl -s "http://localhost:8080/api/assignments/parallel_computing_lab1/submissions" | jq .</pre>
  </div>

  <div class="card">
    <div class="endpoint-header">
      <span class="method-badge">GET</span>
      <span class="endpoint-path">/api/assignments/{id}/submissions/{student_id}/history</span>
    </div>
    <div class="endpoint-desc">查看某个学生多次提交更正的历史记录与时间线（溯源争议提交）。</div>
    <pre class="curl-sample">curl -s "http://localhost:8080/api/assignments/parallel_computing_lab1/submissions/240809010501/history" | jq .</pre>
  </div>

  <!-- 基础系统接口 -->
  <div class="section-title">⚙️ 系统基础与原始附件检索</div>

  <div class="card">
    <div class="endpoint-header">
      <span class="method-badge">GET</span>
      <span class="endpoint-path">/health</span>
    </div>
    <div class="endpoint-desc">公开探活接口，展示运行时间与 IMAP 实时长连接状态。</div>
    <pre>curl -s "http://localhost:8080/health"</pre>
  </div>

  <div class="card">
    <div class="endpoint-header">
      <span class="method-badge">GET</span>
      <span class="endpoint-path">/api/attachments</span>
    </div>
    <div class="endpoint-desc">按关键词、发件人或时间范围检索全邮箱原始附件列表（带分页）。</div>
    <pre class="curl-sample">curl -s "http://localhost:8080/api/attachments?keyword=实验1&page=1&limit=20"</pre>
  </div>
</div>

<script>
  function updateTokenUrls() {
    const token = document.getElementById('tokenInput').value.trim();
    const links = document.querySelectorAll('.dynamic-link');
    links.forEach(link => {
      const base = link.getAttribute('href').split('?')[0];
      link.href = token ? (base + '?token=' + encodeURIComponent(token)) : base;
    });

    const samples = document.querySelectorAll('.curl-sample');
    samples.forEach(sample => {
      let text = sample.textContent;
      // remove old token params
      text = text.replace(/([?&])token=[^"& ]+/g, '');
      text = text.replace(/-H "X-API-Key: [^"]+" /g, '');
      if (token) {
        if (text.includes('?')) {
          text = text.replace('?', '?token=' + token + '&');
        } else if (text.includes(' -o') || text.includes(' -O') || text.includes(' | jq')) {
          text = text.replace(/("http[^"]+")/, function(match) {
            return match.slice(0, -1) + '?token=' + token + '"';
          });
        }
      }
      sample.textContent = text;
    });
  }
</script>
</body>
</html>
`
