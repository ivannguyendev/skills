# Personal AI Skills

Một bộ sưu tập các kỹ năng chuẩn Production (Production-Grade Agent Skills) dành cho các AI Assistants (Antigravity, Claude Code, Cursor, Windsurf...). Kho lưu trữ này cung cấp các nguyên tắc kiến trúc, quy chuẩn hard-rules, bẫy lỗi thường gặp (traps & anti-patterns), và các playbook xử lý sự cố thực tế thay vì những đoạn mã mẫu chung chung.

---

## Danh mục Skills (Skill Catalog)

| Skill | Lĩnh vực | Kích hoạt khi nào / Triggers |
| :--- | :--- | :--- |
| **[`brainstorming`](./brainstorming/)** | Workflow & Design | Lên ý tưởng, phân tích yêu cầu kỹ thuật, so sánh phương án kiến trúc trước khi viết code (ví dụ: `/brainstorming`, lên ý tưởng, thiết kế, đề xuất giải pháp). |
| **[`commit`](./commit/)** | Git & Workflow | Tự động tạo git commit theo chuẩn Conventional Commits, tóm tắt bullet-points, tự động chặn commit trực tiếp trên nhánh `main`/`master`. |
| **[`go-lang`](./go-lang/)** | Backend & Concurrency | Viết, refactor hoặc review Go chuẩn production: chống rò rỉ goroutine, tối ưu cấp phát bộ nhớ (allocations), gRPC/Protobuf, OTel tracing và microservice skeleton mẫu. |
| **[`javascript`](./javascript/)** | Fullstack & Node.js | Xử lý sự cố Node.js/JS production: memory leak, event-loop lag, API timeouts, retry với exponential backoff, xử lý tiền tệ/số lớn an toàn, graceful shutdown. |
| **[`typescript`](./typescript/)** | Language & Typing | Strict mode, kiểu dữ liệu chuẩn xác, schema parsing tại ranh giới (Zod), discriminated unions, dọn dẹp `any`/ép kiểu `as` sai lầm, tối ưu tốc độ `tsc`. |
| **[`mongo`](./mongo/)** | Database | Thiết kế schema (embed vs reference), đánh index chuẩn ESR, loại bỏ triệt để `COLLSCAN`, tối ưu query/aggregation, cấu hình connection pool và xử lý timeout. |
| **[`docker`](./docker/)** | DevOps & Container | Viết Dockerfile tối ưu, multi-stage build, non-root user, container hardening, tối ưu layer caching và giảm thiểu kích thước image. |
| **[`websocket`](./websocket/)** | Networking & Realtime | Xây dựng kết nối realtime bền bỉ, heartbeat (ping/pong), tự động reconnect với jitter, backpressure và quản lý vòng đời kết nối an toàn. |

---

## Cài đặt (Installation)

Bạn có thể tích hợp các skills này vào dự án cá nhân hoặc môi trường làm việc toàn cục:

### 1. Dành cho Google Antigravity
Clone hoặc copy các skills vào thư mục `.agent/skills/` tại workspace của bạn:

```bash
mkdir -p .agent/skills
git clone https://github.com/ivannguyendev/skills.git .agent/skills/personal-skills
```

### 2. Dành cho Claude Code
Thêm trực tiếp vào thư mục `.claude/skills/` của dự án hoặc cấu hình toàn cục tại `~/.claude/skills/`:

```bash
mkdir -p .claude/skills
git clone https://github.com/ivannguyendev/skills.git .claude/skills/personal-skills
```

### 3. Dành cho Cursor / Windsurf / Công cụ khác
Copy các thư mục skill mong muốn vào cấu hình rules/instructions tương ứng của dự án (ví dụ: `.cursorrules`, `.windsurfrules` hoặc thư mục prompt templates).

---

## Cách hoạt động (How It Works)

- **Kích hoạt tự động (Auto-triggering)**: Các AI Assistants hiện đại sẽ tự động đọc phần `description` trong YAML frontmatter của từng file `SKILL.md` để quyết định khi nào cần nạp kỹ năng.
- **Nạp tài liệu theo nhu cầu (On-demand Playbooks)**: Mỗi skill chỉ nạp các file trong `references/` khi gặp đúng vấn đề cụ thể, giúp tối ưu context window và giảm chi phí token.
- **Quy tắc cứng (Hard Rules)**: Các chỉ dẫn ưu tiên tính an toàn và khả năng chịu tải trên môi trường production hơn là viết code chạy được đơn thuần.

---

## Đóng góp (Contributing)

Nếu bạn muốn bổ sung kỹ năng mới hoặc cải thiện các playbook hiện có, hãy mở Issue hoặc gửi Pull Request theo quy chuẩn:
1. Thư mục skill chứa `SKILL.md` với đầy đủ YAML frontmatter và triggers (hỗ trợ cả tiếng Anh lẫn tiếng Việt).
2. Tách các hướng dẫn chuyên sâu vào thư mục `references/` và lập bảng ánh xạ trong `SKILL.md`.
3. Giữ cho skills luôn tinh gọn, tập trung vào giải pháp thực chiến và bẫy lỗi thay vì sao chép tài liệu lý thuyết.
