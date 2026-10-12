package handler

import (
	"fmt"

	"github.com/littlescholar/core/internal/adapter"
	"github.com/littlescholar/core/internal/model"
	"github.com/littlescholar/core/internal/util"
	"gorm.io/gorm"
)

// loadSource 加载存储账号 + 解密凭据 + 取适配器。
//
// 抽成公共函数是因为「资源导入」和「资源映射同步」都要走这三步，
// 其中漏掉任何一步（尤其是解密）都会变成很隐蔽的运行时失败 ——
// 表现是"接口 200 但一个文件都没导入"，而不是报错。
func loadSource(db *gorm.DB, enc *util.Encryptor, accountID uint64) (*model.DiskAccount, *adapter.Credential, adapter.DiskAdapter, error) {
	var acc model.DiskAccount
	if err := db.First(&acc, accountID).Error; err != nil {
		return nil, nil, nil, fmt.Errorf("存储账号不存在")
	}
	a, err := adapter.Get(adapter.DiskType(acc.DiskType))
	if err != nil {
		return nil, nil, nil, err
	}
	cookie, err := enc.Decrypt(acc.CookieEncrypted)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("凭据解密失败")
	}
	extra := make(map[string]string, len(acc.Extra))
	for k, v := range acc.Extra {
		if s, ok := v.(string); ok {
			extra[k] = s
		}
	}
	return &acc, &adapter.Credential{
		DiskType: adapter.DiskType(acc.DiskType),
		Cookie:   cookie,
		Extra:    extra,
	}, a, nil
}
