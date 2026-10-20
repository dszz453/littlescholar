package config

import (
	"fmt"
	"strings"

	"github.com/spf13/viper"
)

// Config 全局配置
type Config struct {
	Server    ServerConfig    `mapstructure:"server"`
	Database  DatabaseConfig  `mapstructure:"database"`
	Redis     RedisConfig     `mapstructure:"redis"`
	JWT       JWTConfig       `mapstructure:"jwt"`
	Security  SecurityConfig  `mapstructure:"security"`
	External  ExternalConfig  `mapstructure:"external"`
	Storage   StorageConfig   `mapstructure:"storage"`
	Transcode TranscodeConfig `mapstructure:"transcode"`
	Subtitle  SubtitleConfig  `mapstructure:"subtitle"`
	Alive     AliveConfig     `mapstructure:"alive"`
	Log       LogConfig       `mapstructure:"log"`
}

// SubtitleConfig 字幕相关。
//
// 不配 Key 时联网匹配整条链**静默跳过**（不是禁用字幕功能）：
// 本地同名字幕、容器内嵌轨两条路照常工作，覆盖绝大多数自带字幕的资源。
type SubtitleConfig struct {
	// OpenSubtitlesKey 填了才启用"网上自动匹配"。
	// 免费申请：https://www.opensubtitles.com → API consumers
	OpenSubtitlesKey string `mapstructure:"opensubtitles_key"`

	// ASRModel 语音听写模型名。走的是 OpenAI 兼容的 /audio/transcriptions，
	// 与后台「AI 配置」共用 base_url + key，但**模型要单独指定** ——
	// AI 配置里存的是对话模型（agnes-3.0-flash），拿去转写必然报错。
	ASRModel string `mapstructure:"asr_model"`
	// ASRLanguage 听写语言。这套库是英语学习资料，传 en 比让模型自己猜稳。
	// 留空则自动判断（中文资料也能用）。
	ASRLanguage string `mapstructure:"asr_language"`

	// ASREngine 用哪个引擎听写：auto / local / http / off。
	//
	//	auto  （默认）本地 whisper 可用就本地，否则退回网关；
	//	local 只用本机 whisper.cpp（网关里没有转写模型时用这个）；
	//	http  只用 OpenAI 兼容的 /audio/transcriptions；
	//	off   关掉听写（前三道来源照常工作）。
	ASREngine string `mapstructure:"asr_engine"`
	// WhisperBin whisper-cli 可执行文件路径（镜像里自带）。
	WhisperBin string `mapstructure:"whisper_bin"`
	// WhisperModel ggml 模型路径。也可以填一个目录，自动挑里面最合适的
	// ggml-*.bin —— 换模型时不用改配置。
	WhisperModel string `mapstructure:"whisper_model"`
	// WhisperThreads 推理线程数。默认 3：这台 NAS 只有 4 核，
	// 听写跑起来时孩子多半正在看视频，吃满核会表现为"播放卡顿"。
	WhisperThreads int `mapstructure:"whisper_threads"`
}

type ServerConfig struct {
	Port int    `mapstructure:"port"`
	Mode string `mapstructure:"mode"` // debug / release
}

type DatabaseConfig struct {
	Driver   string `mapstructure:"driver"` // postgres / sqlite
	Host     string `mapstructure:"host"`
	Port     int    `mapstructure:"port"`
	User     string `mapstructure:"user"`
	Password string `mapstructure:"password"`
	Name     string `mapstructure:"name"`
	SSLMode  string `mapstructure:"ssl_mode"`
	FilePath string `mapstructure:"file_path"` // sqlite 模式使用
}

func (d DatabaseConfig) DSN() string {
	if d.Driver == "sqlite" {
		return d.FilePath
	}
	return fmt.Sprintf("host=%s port=%d user=%s password=%s dbname=%s sslmode=%s TimeZone=Asia/Shanghai",
		d.Host, d.Port, d.User, d.Password, d.Name, d.SSLMode)
}

type RedisConfig struct {
	Enabled  bool   `mapstructure:"enabled"`
	Host     string `mapstructure:"host"`
	Port     int    `mapstructure:"port"`
	Password string `mapstructure:"password"`
	DB       int    `mapstructure:"db"`
}

func (r RedisConfig) Addr() string {
	return fmt.Sprintf("%s:%d", r.Host, r.Port)
}

type JWTConfig struct {
	Secret           string `mapstructure:"secret"`
	AccessExpireHrs  int    `mapstructure:"access_expire_hours"`
	RefreshExpireHrs int    `mapstructure:"refresh_expire_hours"`
	Issuer           string `mapstructure:"issuer"`
}

type SecurityConfig struct {
	// CookieEncryptKey 用于 AES-256-GCM 加密网盘 cookie / AI apiKey，必须 32 字节
	CookieEncryptKey string `mapstructure:"cookie_encrypt_key"`
}

type ExternalConfig struct {
	PansouURL    string `mapstructure:"pansou_url"`
	PansouToken  string `mapstructure:"pansou_token"`
	PancheckURL  string `mapstructure:"pancheck_url"`
	PancheckPass string `mapstructure:"pancheck_password"`
}

type StorageConfig struct {
	// 播放中转缓存目录
	MediaDir string `mapstructure:"media_dir"`
	// 转码输出目录
	TranscodeDir string `mapstructure:"transcode_dir"`
}

type TranscodeConfig struct {
	Enabled     bool   `mapstructure:"enabled"`
	Concurrency int    `mapstructure:"concurrency"`
	FFmpegPath  string `mapstructure:"ffmpeg_path"`

	// CacheMaxGB 转码产物缓存上限（GB）。超出后按 LRU 淘汰最久未播放的产物。
	// 设为 0 表示不限制（危险：长跑会吃满磁盘，仅建议调试时用）。
	CacheMaxGB int `mapstructure:"cache_max_gb"`
	// CacheTTLDays 产物闲置天数。超过这个天数没被播放过就清理。
	// 设为 0 表示不按时间淘汰，只受 CacheMaxGB 约束。
	CacheTTLDays int `mapstructure:"cache_ttl_days"`
}

type LogConfig struct {
	Level string `mapstructure:"level"`
}

// AliveConfig 资源存活巡检
type AliveConfig struct {
	// Enabled 是否开启定时巡检。关闭后仍可在后台手动触发。
	Enabled bool `mapstructure:"enabled"`
	// BatchSize 单批送检数量。太大会被对面拒，太小则慢。
	BatchSize int `mapstructure:"batch_size"`
	// IntervalSeconds 每批之间的间隔秒数，避免把检测服务打挂
	IntervalSeconds int `mapstructure:"interval_seconds"`
	// DeadThreshold 连续失败几次才判定资源失效。
	// 设 1 会因偶发抖动误删可用资源，建议 2~3。
	DeadThreshold int `mapstructure:"dead_threshold"`
	// StaleHours 多久未检查的资源需要重新检查
	StaleHours int `mapstructure:"stale_hours"`
}

// Load 加载配置：优先级 环境变量 > 配置文件 > 默认值
func Load(path string) (*Config, error) {
	v := viper.New()

	// 默认值
	// 学习资料网上没有现成字幕，只能自己听写：默认 whisper-1 + 英文。
	v.SetDefault("subtitle.asr_model", "whisper-1")
	v.SetDefault("subtitle.asr_language", "en")
	// 听写引擎：默认 auto —— 镜像里带了 whisper-cli 和模型就用本地，
	// 没带（或模型没挂上）就退回网关，不需要人工干预。
	v.SetDefault("subtitle.asr_engine", "auto")
	v.SetDefault("subtitle.whisper_bin", "/usr/local/bin/whisper-cli")
	v.SetDefault("subtitle.whisper_model", "/app/models/model.bin")
	v.SetDefault("subtitle.whisper_threads", 3)

	v.SetDefault("server.port", 8080)
	v.SetDefault("server.mode", "release")

	v.SetDefault("database.driver", "postgres")
	v.SetDefault("database.host", "localhost")
	v.SetDefault("database.port", 5432)
	v.SetDefault("database.user", "postgres")
	v.SetDefault("database.password", "postgres")
	v.SetDefault("database.name", "littlescholar")
	v.SetDefault("database.ssl_mode", "disable")
	v.SetDefault("database.file_path", "./data/littlescholar.db")

	v.SetDefault("redis.enabled", true)
	v.SetDefault("redis.host", "localhost")
	v.SetDefault("redis.port", 6379)
	v.SetDefault("redis.db", 0)

	v.SetDefault("jwt.secret", "change-me-in-production")
	v.SetDefault("jwt.access_expire_hours", 2)
	v.SetDefault("jwt.refresh_expire_hours", 168)
	v.SetDefault("jwt.issuer", "littlescholar")

	// 字幕：默认不联网（没 key 时自动跳过，不影响本地/内嵌两条路）
	v.SetDefault("subtitle.opensubtitles_key", "")

	v.SetDefault("security.cookie_encrypt_key", "0123456789abcdef0123456789abcdef")

	v.SetDefault("external.pansou_url", "http://localhost:8888")
	v.SetDefault("external.pancheck_url", "http://localhost:6080")

	v.SetDefault("storage.media_dir", "./data/media")
	v.SetDefault("storage.transcode_dir", "./data/transcode")

	v.SetDefault("transcode.enabled", true)
	v.SetDefault("transcode.concurrency", 2)
	v.SetDefault("transcode.ffmpeg_path", "ffmpeg")
	// 20GB：按 720p 产物约 700MB 估算，约可留住 28 部影片，对家用足够。
	v.SetDefault("transcode.cache_max_gb", 20)
	// 30 天：孩子反复看同一批内容，一个月没碰过的基本可以重新转。
	v.SetDefault("transcode.cache_ttl_days", 30)

	v.SetDefault("alive.enabled", true)
	v.SetDefault("alive.batch_size", 30)
	v.SetDefault("alive.interval_seconds", 2)
	// 2 次：单次判定的可靠性有限，网盘限流或对面抖动都会造成假阳性。
	// 设 1 会让偶发失败直接判死，把孩子正在看的资源误标。
	v.SetDefault("alive.dead_threshold", 2)
	v.SetDefault("alive.stale_hours", 72)

	v.SetDefault("log.level", "info")

	if path != "" {
		v.SetConfigFile(path)
		if err := v.ReadInConfig(); err != nil {
			// 配置文件不存在不视为致命错误
			if _, ok := err.(viper.ConfigFileNotFoundError); !ok {
				// 文件存在但解析失败则返回错误
				if !strings.Contains(err.Error(), "no such file") {
					return nil, err
				}
			}
		}
	}

	// 环境变量覆盖：SERVER_PORT -> server.port
	v.SetEnvKeyReplacer(strings.NewReplacer(".", "_"))
	v.AutomaticEnv()

	var cfg Config
	if err := v.Unmarshal(&cfg); err != nil {
		return nil, err
	}
	return &cfg, nil
}
