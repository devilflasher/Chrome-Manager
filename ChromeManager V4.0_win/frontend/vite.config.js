import { defineConfig } from 'vite'
import { copyFileSync, mkdirSync, existsSync, readdirSync } from 'fs'
import { resolve, join } from 'path'

export default defineConfig({
  build: {
    outDir: 'dist',
    rollupOptions: {
      input: 'index.html'
    }
  },
  server: {
    port: 5175,
    strictPort: false
  },
  plugins: [
    {
      name: 'copy-settings',
      writeBundle() {
        // 构建完成后复制设置相关文件到 dist 目录
        try {
          // 创建 src 目录并复制 CSS 文件
          mkdirSync(resolve(__dirname, 'dist', 'src'), { recursive: true });
          
          // 复制 style.css
          copyFileSync(
            resolve(__dirname, 'src', 'style.css'),
            resolve(__dirname, 'dist', 'src', 'style.css')
          );
          
          // 复制 bindings 目录 (重要：设置页面需要用到)
          try {
            const bindingsSource = resolve(__dirname, 'bindings');
            const bindingsDest = resolve(__dirname, 'dist', 'bindings');
            
            // 递归复制 bindings 目录
            function copyDir(src, dest) {
              if (!existsSync(dest)) {
                mkdirSync(dest, { recursive: true });
              }
              
              const entries = readdirSync(src, { withFileTypes: true });
              
              for (let entry of entries) {
                const srcPath = join(src, entry.name);
                const destPath = join(dest, entry.name);
                
                if (entry.isDirectory()) {
                  copyDir(srcPath, destPath);
                } else {
                  copyFileSync(srcPath, destPath);
                }
              }
            }
            
            if (existsSync(bindingsSource)) {
              copyDir(bindingsSource, bindingsDest);
            }
          } catch (bindingError) {
            console.error('❌ Failed to copy bindings:', bindingError.message);
          }
        } catch (error) {
          console.error('❌ Failed to copy files:', error);
          console.error('Error details:', error.message);
        }
      }
    }
  ]
}) 
