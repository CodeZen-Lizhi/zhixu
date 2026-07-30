# W6 Smoke Image Cleanup Result

- Executed: 2026-07-30 20:59:02 CST
- Command: `deploy/cleanup-smoke-images.sh --apply --confirm DELETE_UNUSED_ZHIXU_SMOKE_IMAGES`
- Dry-run target count: 282 exact Compose-owned image tags
- Dry-run referenced target image IDs: 0
- Result: 282 target tags removed; post-cleanup exact target count is 0
- Non-target image inventory SHA-256 before/after: `12306662d8b564c6dbb6e280748198002806118cee454ed5a43df648813bea80`
- Protected volume: `deploy_zhixu-postgres` retained with the same mountpoint and Compose labels
- Protected runtime containers retained with the same IDs and healthy state:
  - app: `77557ccd1f6fb39e8d0fdbb6a802046dceefc52472bdad37177752d0ba32e3c2`
  - worker: `2ca8177c3227b2678a4d49bf3273d82a85756298b299e16e5ab0e3dc7d237238`
  - proxy: `b2b6c884c650f63bef99e4df02a865f1d148ef668b923853675cbed16818cd41`
  - postgres: `99b0a81d03ab54d4318c36fa5d202d1ed892c06079abba9c20f140e3092b17cf`
- Docker events during cleanup contained image untag/delete operations and routine health-check execs; no protected container stop/destroy or volume removal occurred.
- Global container/volume inventory hashes were not used as preservation evidence because parallel PostgreSQL integration tests created and removed temporary resources during the cleanup window.
