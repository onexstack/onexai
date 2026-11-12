#!/bin/bash

# ==========================================
# Configuration
# ==========================================

# Define database connection variables
# Note: handling of special characters in the password is kept as-is
DB_CMD='mariadb -h 127.0.0.1 -P 3306 -uonex -p'\''CHANGE_ME'\'''
DB_NAME='onex'

# ==========================================
# Function definitions
# ==========================================

insert_cronjob() {
    echo "[INFO] Inserting into dms_cronjob ..."

    eval "$DB_CMD" -D "$DB_NAME" <<EOF
INSERT INTO dms_cronjob (
    cron_job_id,
    username,
    scope,
    name,
    description,
    schedule,
    status,
    concurrency_policy,
    suspend,
    job_template,
    success_history_limit,
    failed_history_limit
) VALUES (
    UUID(),
    'admin',
    'llm',
    'test-second-job',
    'Run every second',
    '* * * * * *',
    NULL,
    1,
    0,
    '{
        "username": "admin",
        "scope": "llm",
        "name": "test-second-job-instance",
        "description": "Generated from cronjob",
        "watcher": "llmtrain",
        "suspend": 0,
        "params": {"train":{"idempotentExecution":1,"jobTimeout":15,"batchSize":2}},
        "status": "Pending"
    }',
    3,
    1
);
EOF

    if [ $? -eq 0 ]; then
        echo "[SUCCESS] Data successfully inserted into dms_cronjob."
    else
        echo "[ERROR] failed to insert into dms_cronjob."
        exit 1
    fi
}

insert_job() {
    echo "[INFO] Inserting into dms_job ..."

    eval "$DB_CMD" -D "$DB_NAME" <<EOF
INSERT INTO dms_job (
    job_id,
    username,
    scope,
    name,
    description,
    cron_job_id,
    watcher,
    suspend,
    params,
    results,
    status,
    created_at,
    updated_at
) VALUES (
    UUID(),
    'colin',
    'llm',
    'test-single-job',
    'A single execution job',
    '',
    'llmtrain',
    0,
    '{
        "idempotentExecution": 1,
        "jobTimeout": 3600,
        "batchSize": 8,
        "learningRate": 0.001
    }',
    '{}',
    'Pending',
    NOW(),
    NOW()
);
EOF

    if [ $? -eq 0 ]; then
        echo "[SUCCESS] Data successfully inserted into dms_job."
    else
        echo "[ERROR] failed to insert into dms_job."
        exit 1
    fi
}

usage() {
    echo "Usage: $0 {cronjob|job|all}"
    echo "  cronjob : Insert data into dms_cronjob table only"
    echo "  job     : Insert data into dms_job table only"
    echo "  all     : Insert data into both tables"
    exit 1
}

# ==========================================
# Main logic
# ==========================================

# Check the number of arguments
if [ $# -eq 0 ]; then
    usage
fi

ACTION=$1

case "$ACTION" in
    cronjob)
        insert_cronjob
        ;;
    job)
        insert_job
        ;;
    all)
        insert_cronjob
        echo "----------------------------------------"
        insert_job
        ;;
    *)
        echo "[ERROR] Invalid argument: $ACTION"
        usage
        ;;
esac

echo "Operation completed."
