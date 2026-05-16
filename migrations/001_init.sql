-- LinkCode database schema v1

CREATE TABLE IF NOT EXISTS bots (
    id          BIGINT AUTO_INCREMENT PRIMARY KEY,
    bot_id      VARCHAR(128)  NOT NULL COMMENT '企业微信 Bot ID',
    bot_name    VARCHAR(255)  NOT NULL DEFAULT '' COMMENT '用户给 Bot 起的名字',
    bot_secret_encrypted VARBINARY(512) NOT NULL COMMENT 'AES-256-GCM 加密的 Secret',
    status      ENUM('idle', 'bound', 'unavailable') NOT NULL DEFAULT 'idle',
    bound_session_id BIGINT DEFAULT NULL,
    created_at  DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    last_used_at DATETIME DEFAULT NULL,
    UNIQUE KEY uk_bot_id (bot_id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;

CREATE TABLE IF NOT EXISTS sessions (
    id              BIGINT AUTO_INCREMENT PRIMARY KEY,
    name            VARCHAR(255) NOT NULL DEFAULT '' COMMENT '用户给 Session 起的名字',
    agent_type      VARCHAR(64)  NOT NULL DEFAULT 'claude-code',
    process_status  ENUM('waked', 'sleeped') NOT NULL DEFAULT 'waked',
    claude_session_id VARCHAR(255) DEFAULT '' COMMENT 'Claude 的 Session ID，用于 resume',
    bound_bot_id    BIGINT DEFAULT NULL,
    created_at      DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    last_active_at  DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    FOREIGN KEY (bound_bot_id) REFERENCES bots(id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;

CREATE TABLE IF NOT EXISTS messages (
    id           BIGINT AUTO_INCREMENT PRIMARY KEY,
    session_id   BIGINT NOT NULL,
    role         ENUM('user', 'agent') NOT NULL,
    content      TEXT NOT NULL,
    content_type VARCHAR(32) NOT NULL DEFAULT 'text',
    created_at   DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    FOREIGN KEY (session_id) REFERENCES sessions(id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;
