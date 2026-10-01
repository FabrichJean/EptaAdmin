// Usage:
//   ./deploy.sh        build + restart (the usual workflow)
//   pm2 start ecosystem.config.js   first start only
module.exports = {
  apps: [
    {
      name: "eptaadmin",
      cwd: __dirname,
      script: "./eptaadmin-linux",
      interpreter: "none",
      exec_mode: "fork",
      autorestart: true,
    },
  ],
};
