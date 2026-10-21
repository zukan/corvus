CREATE TABLE IF NOT EXISTS organizations (
 id VARCHAR(191) PRIMARY KEY, name VARCHAR(255) NOT NULL, email VARCHAR(320) NOT NULL, secret TEXT NOT NULL,
 created_at DATETIME(3) NOT NULL, updated_at DATETIME(3) NOT NULL, deleted BOOLEAN NOT NULL DEFAULT FALSE, deleted_at DATETIME(3) NULL,
 UNIQUE KEY uq_organizations_email (email)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_bin;
CREATE TABLE IF NOT EXISTS admins (
 id VARCHAR(191) PRIMARY KEY, organization_id VARCHAR(191) NOT NULL, fullname VARCHAR(255) NOT NULL, email VARCHAR(320) NOT NULL, password TEXT NOT NULL,
 created_at DATETIME(3) NOT NULL, updated_at DATETIME(3) NOT NULL,
 UNIQUE KEY uq_admins_org_email (organization_id,email), KEY idx_admins_org (organization_id,id),
 CONSTRAINT fk_admins_org FOREIGN KEY (organization_id) REFERENCES organizations(id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_bin;
CREATE TABLE IF NOT EXISTS applications (
 id VARCHAR(191) PRIMARY KEY, organization_id VARCHAR(191) NOT NULL, created_by VARCHAR(191) NOT NULL, name VARCHAR(255) NOT NULL,
 created_at DATETIME(3) NOT NULL, updated_at DATETIME(3) NOT NULL, deleted BOOLEAN NOT NULL DEFAULT FALSE, deleted_at DATETIME(3) NULL,
 UNIQUE KEY uq_applications_tenant_id (organization_id,id), UNIQUE KEY uq_applications_org_name (organization_id,name), KEY idx_applications_tenant (organization_id,deleted,id),
 CONSTRAINT fk_applications_org FOREIGN KEY (organization_id) REFERENCES organizations(id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_bin;
CREATE TABLE IF NOT EXISTS comments (
 id VARCHAR(191) PRIMARY KEY, application_id VARCHAR(191) NOT NULL, owner_id MEDIUMTEXT NOT NULL, content MEDIUMTEXT NOT NULL,
 ref_id MEDIUMTEXT NULL, origin MEDIUMTEXT NULL, replies JSON NOT NULL, flags JSON NOT NULL, up_votes JSON NOT NULL, down_votes JSON NOT NULL,
 created_at DATETIME(3) NOT NULL, updated_at DATETIME(3) NOT NULL, deleted BOOLEAN NOT NULL DEFAULT FALSE, deleted_at DATETIME(3) NULL,
 KEY idx_comments_tenant_order (application_id,deleted,created_at,id), KEY idx_comments_tenant_owner (application_id,owner_id(191),deleted,created_at,id),
 KEY idx_comments_tenant_ref (application_id,ref_id(191),deleted,created_at,id), KEY idx_comments_tenant_origin (application_id,origin(191),deleted,created_at,id),
 CONSTRAINT fk_comments_application FOREIGN KEY (application_id) REFERENCES applications(id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_bin;
CREATE TABLE IF NOT EXISTS comment_replies (
 id VARCHAR(191) PRIMARY KEY, comment_id VARCHAR(191) NOT NULL, owner_id MEDIUMTEXT NOT NULL, content MEDIUMTEXT NOT NULL,
 flags JSON NOT NULL, up_votes JSON NOT NULL, down_votes JSON NOT NULL, created_at DATETIME(3) NOT NULL, updated_at DATETIME(3) NOT NULL,
 deleted BOOLEAN NOT NULL DEFAULT FALSE, deleted_at DATETIME(3) NULL, KEY idx_replies_comment (comment_id,deleted,created_at,id), KEY idx_replies_owner (comment_id,owner_id(191),deleted,id),
 CONSTRAINT fk_replies_comment FOREIGN KEY (comment_id) REFERENCES comments(id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_bin;
