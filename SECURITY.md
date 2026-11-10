# Chính sách bảo mật / Security policy

## Báo lỗ hổng

**Đừng mở issue công khai** cho lỗ hổng bảo mật.

Hãy báo riêng qua GitHub: tab **Security → Report a vulnerability**
(<https://github.com/Project-by-Myself/clirouter/security/advisories/new>).

Nên kèm:

- phiên bản / commit, hệ điều hành, cách chạy (bind address, có `admin_password` / `api_keys` không);
- các bước tái hiện hoặc PoC tối thiểu;
- ảnh hưởng (lộ key provider, chiếm admin API, DoS, ...).

**Không gửi API key, token OAuth hay file `config.yaml` thật.** Dùng key giả.

Chúng tôi sẽ phản hồi trong vòng 7 ngày, thống nhất mốc công bố với bạn (mặc định tối đa 90 ngày)
và ghi công trong release note nếu bạn muốn.

## Phạm vi

Trong phạm vi: binary `clirouter`, admin API `/admin/api/*`, dashboard nhúng, xử lý và lưu trữ
key/token, luồng đăng nhập OAuth.

Ngoài phạm vi:

- kẻ tấn công đã đọc/ghi được `~/.clirouter` hoặc chạy code với quyền user của bạn;
- người có `admin_password` (admin được tin cậy: có thể đổi `base_url` provider);
- OAuth client ID/secret trong `internal/auth/endpoints.go` — đó là giá trị **công khai** của các
  ứng dụng cài đặt (gemini-cli, Copilot, Claude), không phải bí mật;
- việc provider từ chối/khoá tài khoản do dùng subscription qua proxy.

## Phiên bản được hỗ trợ

Chỉ nhánh `main` / bản phát hành mới nhất nhận bản vá bảo mật.

---

To report a vulnerability, use GitHub private vulnerability reporting (Security → Report a
vulnerability). Do not open a public issue and do not include real credentials.
