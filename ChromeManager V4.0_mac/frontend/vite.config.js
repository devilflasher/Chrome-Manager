import { defineConfig } from 'vite'
import { copyFileSync, mkdirSync, existsSync, readdirSync } from 'fs'
import { resolve, join } from 'path'

export default defineConfig({
  build: {
    outDir: 'dist',
    rollupOptions: {
      input: resolve(__dirname, 'index.html')
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
        console.log('🔧 Starting file copy process...');
        try {
          // 创建 src 目录并复制 CSS 文件
          mkdirSync(resolve(__dirname, 'dist', 'src'), { recursive: true });
          
          // 复制 style.css
          copyFileSync(
            resolve(__dirname, 'src', 'style.css'),
            resolve(__dirname, 'dist', 'src', 'style.css')
          );
          console.log('✅ style.css copied to dist/src directory');
          
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
              console.log('✅ bindings directory copied to dist directory');
            } else {
              console.log('⚠️ bindings directory not found, skipping copy');
            }
          } catch (bindingError) {
            console.error('❌ Failed to copy bindings:', bindingError.message);
          }
          
          console.log('🎉 All files copied successfully!');
        } catch (error) {
          console.error('❌ Failed to copy files:', error);
          console.error('Error details:', error.message);
        }
      }
    }
  ]
}) 