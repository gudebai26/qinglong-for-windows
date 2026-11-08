package store

// schemaStatements is the authoritative DDL for data/db/database.sqlite.
//
// It was copied verbatim from the real database created by the Node panel
// (sqlite_master of a freshly bootstrapped 2.24.0 instance), not from the
// Sequelize model names: tables are PascalCase and pluralized, columns keep
// their camelCase createdAt/updatedAt spelling.
//
// Every statement is idempotent so that taking over an existing data/ directory
// is a no-op, while a fresh data/ directory still gets a schema Node can use.
var schemaStatements = []string{
	"CREATE TABLE IF NOT EXISTS `Apps` (`id` INTEGER PRIMARY KEY AUTOINCREMENT, `name` VARCHAR(255), `scopes` JSON, `client_id` VARCHAR(255), `client_secret` VARCHAR(255), `tokens` JSON, `createdAt` DATETIME NOT NULL, `updatedAt` DATETIME NOT NULL, UNIQUE (`name`))",

	"CREATE TABLE IF NOT EXISTS `Auths` (`id` INTEGER PRIMARY KEY AUTOINCREMENT, `ip` VARCHAR(255), `type` VARCHAR(255), `info` JSON, `createdAt` DATETIME NOT NULL, `updatedAt` DATETIME NOT NULL)",

	"CREATE TABLE IF NOT EXISTS `CrontabStats` (`id` INTEGER PRIMARY KEY AUTOINCREMENT, `ref_id` NUMBER NOT NULL, `date` VARCHAR(255) NOT NULL, `run_count` NUMBER DEFAULT 0, `success_count` NUMBER DEFAULT 0, `fail_count` NUMBER DEFAULT 0, `total_time` NUMBER DEFAULT 0, `max_time` NUMBER DEFAULT 0, `createdAt` DATETIME NOT NULL, `updatedAt` DATETIME NOT NULL)",

	"CREATE TABLE IF NOT EXISTS `CrontabViews` (`id` INTEGER PRIMARY KEY AUTOINCREMENT, `name` VARCHAR(255), `position` NUMBER, `isDisabled` NUMBER, `filters` JSON, `sorts` JSON, `filterRelation` VARCHAR(255), `type` NUMBER, `createdAt` DATETIME NOT NULL, `updatedAt` DATETIME NOT NULL, UNIQUE (`name`))",

	"CREATE TABLE IF NOT EXISTS `Crontabs` (`id` INTEGER PRIMARY KEY AUTOINCREMENT, `name` VARCHAR(255), `command` VARCHAR(255), `schedule` VARCHAR(255), `timestamp` VARCHAR(255), `saved` TINYINT(1), `status` NUMBER, `isSystem` NUMBER, `pid` NUMBER, `isDisabled` NUMBER, `isPinned` NUMBER, `log_path` VARCHAR(255), `queued_token` VARCHAR(255), `labels` JSON, `last_running_time` NUMBER, `last_execution_time` NUMBER, `sub_id` NUMBER, `extra_schedules` JSON, `task_before` VARCHAR(255), `task_after` VARCHAR(255), `log_name` VARCHAR(255), `allow_multiple_instances` NUMBER, `work_dir` VARCHAR(255), `createdAt` DATETIME NOT NULL, `updatedAt` DATETIME NOT NULL, UNIQUE (`name`, `command`, `schedule`))",

	"CREATE TABLE IF NOT EXISTS `Dependences` (`id` INTEGER PRIMARY KEY AUTOINCREMENT, `name` VARCHAR(255), `type` NUMBER, `timestamp` VARCHAR(255), `status` NUMBER, `log` JSON, `remark` VARCHAR(255), `createdAt` DATETIME NOT NULL, `updatedAt` DATETIME NOT NULL)",

	"CREATE TABLE IF NOT EXISTS `Envs` (`id` INTEGER PRIMARY KEY AUTOINCREMENT, `value` VARCHAR(255), `timestamp` VARCHAR(255), `status` NUMBER, `position` NUMBER, `name` VARCHAR(255), `remarks` VARCHAR(255), `isPinned` NUMBER, `labels` JSON, `createdAt` DATETIME NOT NULL, `updatedAt` DATETIME NOT NULL, UNIQUE (`value`, `name`))",

	"CREATE TABLE IF NOT EXISTS `RunningInstances` (`id` INTEGER PRIMARY KEY AUTOINCREMENT, `cron_id` NUMBER NOT NULL, `pid` NUMBER, `log_path` VARCHAR(255), `started_at` NUMBER NOT NULL, `finished_at` NUMBER, `status` NUMBER NOT NULL DEFAULT 0, `exit_code` NUMBER, `createdAt` DATETIME NOT NULL, `updatedAt` DATETIME NOT NULL)",

	// Node keeps its own column-level migration bookkeeping here; the Go side
	// only guarantees the table exists so a fresh data/ stays Node compatible.
	"CREATE TABLE IF NOT EXISTS \"SchemaMigrations\" (\"id\" TEXT PRIMARY KEY, \"applied_at\" TEXT NOT NULL)",

	"CREATE TABLE IF NOT EXISTS `Subscriptions` (`id` INTEGER PRIMARY KEY AUTOINCREMENT, `name` VARCHAR(255), `url` VARCHAR(255), `schedule` VARCHAR(255), `interval_schedule` JSON, `type` VARCHAR(255), `whitelist` VARCHAR(255), `blacklist` VARCHAR(255), `status` NUMBER, `dependences` VARCHAR(255), `extensions` VARCHAR(255), `sub_before` VARCHAR(255), `sub_after` VARCHAR(255), `branch` VARCHAR(255), `pull_type` VARCHAR(255), `pull_option` JSON, `pid` NUMBER, `is_disabled` NUMBER, `log_path` VARCHAR(255), `schedule_type` VARCHAR(255), `alias` VARCHAR(255), `proxy` VARCHAR(255), `autoAddCron` NUMBER, `autoDelCron` NUMBER, `createdAt` DATETIME NOT NULL, `updatedAt` DATETIME NOT NULL, UNIQUE (`name`, `url`, `schedule`, `interval_schedule`), UNIQUE (`alias`))",

	"CREATE INDEX IF NOT EXISTS `crontab_stats_date` ON `CrontabStats` (`date`)",
	"CREATE UNIQUE INDEX IF NOT EXISTS `crontab_stats_ref_id_date` ON `CrontabStats` (`ref_id`, `date`)",
}

// expectedTables lists every table a fully migrated database must contain.
var expectedTables = []string{
	"Apps",
	"Auths",
	"CrontabStats",
	"CrontabViews",
	"Crontabs",
	"Dependences",
	"Envs",
	"RunningInstances",
	"SchemaMigrations",
	"Subscriptions",
}
