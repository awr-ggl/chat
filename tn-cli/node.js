const express = require('express');
const { spawn } = require('child_process');

const app = express();
app.use(express.json());
const port = process.argv[2] || 3000;
const ip = process.argv[3] || '0.0.0.0';
const tinode_grpc_server = process.argv[4] || 'localhost:16060'; // 34.101.45.102:16060
app.post('/execute', (req, res) => {
    console.log(req.body)
    // Request Body: {"script": "chatdemo --what cred --group grppQw179RgniM usrMSeBdwoBrJ8,usrMDDxrn4YuLg,usrHKiDc0XQudY\n"}
    const tinode_script = req.body.script;
    // bob is ROOT. How? See tinode-db/README.md
    let pythonProcess = spawn('python', ['tn-cli.py', '--host', tinode_grpc_server, '--verbose', '--login-basic','bob:bob123']);

    pythonProcess.stdin.write(tinode_script);
    pythonProcess.stdin.end();

    pythonProcess.stdout.on('data', (data) => {
        console.log(`stdout: ${data}`);
    });

    pythonProcess.stderr.on('data', (data) => {
        console.error(`stderr: ${data}`);
    });

    pythonProcess.on('close', (code) => {
        console.log(`child process exited with code ${code}`);
        res.send(`child process exited with code ${code}`);
    });
});

app.listen(port, ip, () => {
    console.log(`Server is running on port ${port}`);
});
