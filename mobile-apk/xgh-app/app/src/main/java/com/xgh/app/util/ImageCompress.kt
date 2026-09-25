package com.xgh.app.util

import android.content.Context
import android.graphics.Bitmap
import android.graphics.BitmapFactory
import java.io.File

/**
 * 上报图片压缩：长边压到 1280px、JPEG 80。
 * 手机原图 3-8MB，32 人并发上传会挤爆上行带宽；压缩后单张约 150-300KB。
 */
object ImageCompress {

    fun compressToTemp(context: Context, src: File, maxDim: Int = 1280, quality: Int = 80): File? {
        if (!src.exists()) return null
        // 只读尺寸，先算采样率
        val bounds = BitmapFactory.Options().apply { inJustDecodeBounds = true }
        BitmapFactory.decodeFile(src.absolutePath, bounds)
        if (bounds.outWidth <= 0 || bounds.outHeight <= 0) return null

        var sample = 1
        while (bounds.outWidth / (sample * 2) >= maxDim || bounds.outHeight / (sample * 2) >= maxDim) {
            sample *= 2
        }
        val decoded = BitmapFactory.decodeFile(
            src.absolutePath,
            BitmapFactory.Options().apply { inSampleSize = sample }
        ) ?: return null

        val longEdge = maxOf(decoded.width, decoded.height)
        val bitmap = if (longEdge > maxDim) {
            val scale = maxDim.toFloat() / longEdge
            Bitmap.createScaledBitmap(
                decoded,
                (decoded.width * scale).toInt().coerceAtLeast(1),
                (decoded.height * scale).toInt().coerceAtLeast(1),
                true
            ).also { if (it !== decoded) decoded.recycle() }
        } else {
            decoded
        }

        val dir = File(context.cacheDir, "camera").apply { mkdirs() }
        val out = File(dir, "up_${System.currentTimeMillis()}.jpg")
        try {
            out.outputStream().use { bitmap.compress(Bitmap.CompressFormat.JPEG, quality, it) }
        } catch (_: Exception) {
            return null
        } finally {
            bitmap.recycle()
        }
        return out
    }
}
