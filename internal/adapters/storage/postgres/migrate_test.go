package postgres

import (
	"strings"
	"testing"
)

func TestEmbeddedMigrationsAreOrdered(t *testing.T) {
	migrations, err := loadMigrations()
	if err != nil {
		t.Fatal(err)
	}
	if len(migrations) == 0 {
		t.Fatal("没有嵌入任何迁移")
	}
	for index, value := range migrations {
		if strings.TrimSpace(value.sql) == "" {
			t.Fatalf("迁移 %s 为空", value.name)
		}
		if index > 0 && migrations[index-1].version >= value.version {
			t.Fatalf("迁移版本无序: %+v", migrations)
		}
	}
}
