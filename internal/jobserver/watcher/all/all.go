package all

import (
	_ "github.com/onexstack/onexai/internal/jobserver/watcher/cronjob/cronjob"
	_ "github.com/onexstack/onexai/internal/jobserver/watcher/cronjob/statesync"
	_ "github.com/onexstack/onexai/internal/jobserver/watcher/customized/fake"
	_ "github.com/onexstack/onexai/internal/jobserver/watcher/job/llmtrain"
)
