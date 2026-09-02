package webhook

// Real public keys, one per type the platform accepts. They are generated
// rather than invented: a made-up string of roughly the right shape passes a
// regex and fails a parser, and the parser is what runs in production.
//
// The five ordinary types came from ssh-keygen. The two sk-* forms normally
// need a hardware token, so their wire format was assembled by hand and then
// verified through the same ssh.ParseAuthorizedKey the validator uses.
var validPublicKeys = map[string]string{
	"ssh-rsa":                            "ssh-rsa AAAAB3NzaC1yc2EAAAADAQABAAABgQCr0DLbR4mDGleWAFLiyLyjVXiAB2JPzf8mUPO8e496mkbSfUmpesOMksfdEQMtYIkKdzkrAnS+YbmkbFZW0GVbkH/qQR7qR1w5nrDPEBqCIz4DcDgytBjKua8kQ9Adzdb+KGyq6ZHD0g4SQhRclKQ7NddP7Mf6a4Yc7eKS6jOT7UEaj/27WfeK0hq0c0dRP6fBGCxuGBAZmilMG5UbbzDEnhi5xtPDFhu2FyOJCB3EPxKzVY+ke3J2R8JE7JCpyRyilFX5So/0ek/FhJ4h8jFC1eUOsPYeusNpeJAj2x0YGYrpi0ij5iN7mUO8XDNp9jpN6/66gf8FwJXDNp9LE9nHX6sYo5oj6NP1toMuSI+b2JIXUOzF036MXIr34lSopu8nJoIcf9lO6nl/sAJPeIBQIai3E70X7tFHuI5cJYfczWIyiBg354LwpUBtQp0GEDTb92REedsdP1e9hlcGPyxSAlL7XTirTF9OY51JhPulpu/B0DiUpnR4FQxtvYYDp88= alice@laptop",
	"ecdsa-sha2-nistp256":                "ecdsa-sha2-nistp256 AAAAE2VjZHNhLXNoYTItbmlzdHAyNTYAAAAIbmlzdHAyNTYAAABBBCgRe7fwU3yCCf2j2osJDaom0vxOTnFzxOvYPO6Azt5DLRtso7ZtrmmGQ7UtGifatCnpbMmyHvA+izUVRwSjmRc= alice@laptop",
	"ecdsa-sha2-nistp384":                "ecdsa-sha2-nistp384 AAAAE2VjZHNhLXNoYTItbmlzdHAzODQAAAAIbmlzdHAzODQAAABhBFf8X19TJnGuRKbjjCJ9odMpwU96yfsZfHl97N1RPh88L4wxmvxBFOz+PK5/NZNKWU5hK49t2ZPN94qcpEEvE0DoVoGkr2PlhjkHnPE+JiHX+cn3fqsFmLlanp8WGBI/8Q== alice@laptop",
	"ecdsa-sha2-nistp521":                "ecdsa-sha2-nistp521 AAAAE2VjZHNhLXNoYTItbmlzdHA1MjEAAAAIbmlzdHA1MjEAAACFBAFt8PLBPFGd8MinERaCe4ud/qnSnRlcXzVwPsp2AMIxEOT1owhNrB3QlHJUaY8XAZacPf/eABS5NvYoUMpM9Bwq0wEbgrmBwTrAfzqk5Pezi3O1PvaitUlKHiLxSd7Y/O/f6A6ZeFUZZV8NcRcbGbnlxcU/taIP3cgk59Ao2bjsRpoFVw== alice@laptop",
	"ssh-ed25519":                        "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIBzqLX1fugjIfLRPWOBujokgUYuEODsP4SjjOgSqP4cc alice@laptop",
	"sk-ssh-ed25519@openssh.com":         "sk-ssh-ed25519@openssh.com AAAAGnNrLXNzaC1lZDI1NTE5QG9wZW5zc2guY29tAAAAIPgDBii2cBHTBCyDdPV5FWLXMlAIZ1RzI11Bycvth82EAAAABHNzaDo= alice@token",
	"sk-ecdsa-sha2-nistp256@openssh.com": "sk-ecdsa-sha2-nistp256@openssh.com AAAAInNrLWVjZHNhLXNoYTItbmlzdHAyNTZAb3BlbnNzaC5jb20AAAAIbmlzdHAyNTYAAABBBE5+j95VQm+Vh2Dxssp89fX0OFsoAL3n2Pawgf4hFlZeVW5nFVqTBI8N999445hzNtAnF2b8BkTNe+GHf4H9U8YAAAAEc3NoOg== alice@token",
}
