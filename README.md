# Personal AI Skills

Một bộ sưu tập các kỹ năng (skills/guidelines) cá nhân thường dùng dành cho các AI Assistants (như Antigravity, Claude, v.v.) nhằm tự động hóa các tác vụ lập trình và tăng hiệu suất.

## Cài đặt (Installation)

Bạn có thể thêm các skills này vào dự án của mình bằng cách clone repository hoặc copy trực tiếp các thư mục.

### 1. Dành cho hệ thống hỗ trợ `.agent/skills` (ví dụ: Antigravity)

Chạy lệnh sau tại thư mục gốc dự án của bạn:

```bash
mkdir -p .agent/skills
git clone https://github.com/ivannguyendev/skills.git .agent/skills/personal-skills
```

### 2. Dành cho Claude

Copy nội dung từ file `CLAUDE.md` trong repo này vào file `CLAUDE.md` ở dự án của bạn, hoặc cấu hình thư mục `.claude` tương ứng.

## Sử dụng (Usage)

Khi các skills đã được cài đặt vào đúng thư mục, AI Assistant của bạn sẽ tự động nạp các tập tin hướng dẫn (`SKILL.md` hoặc `CLAUDE.md`). Bạn có thể kích hoạt chúng bằng cách yêu cầu trực tiếp trong chat (ví dụ: yêu cầu dùng skill `commit` hoặc `/brainstorming`).

Các skills hiện có trong repo (thường xuyên được cập nhật):

- `brainstorming`
- `commit`
- `docker`
- `go-lang`
- `websocket`

_(Lưu ý: Bạn không cần cấu hình chi tiết cho từng skill, AI sẽ tự động đọc nội dung khi được gọi)._

## Đóng góp (Contributing)

Nếu bạn có cải tiến hoặc muốn chia sẻ một skill mới, cứ tự nhiên mở Issue hoặc Pull Request!
