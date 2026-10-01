// Usage:
//   ./deploy.sh                      restart (the usual workflow)
//   pm2 start ecosystem.config.js    first start only
//
// script points at start.sh, not the binary directly — start.sh rebuilds
// from source before every (re)start, so pm2 can never run a stale binary
// just because someone ran `pm2 restart` without going through deploy.sh
// first.
module.exports = {
  apps: [
    {
      name: "eptaadmin",
      cwd: __dirname,
      script: "./start.sh",
      interpreter: "none",
      exec_mode: "fork",
      autorestart: true,
    },
  ],
};
