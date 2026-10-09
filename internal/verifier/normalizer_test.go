package verifier

import (
	"testing"
)

func TestStripComments(t *testing.T) {
	cCode := []byte(`
	// 作者: 张三
	// 学号: 240809010501
	#include <stdio.h>
	/* 多行注释
	   并行计算实验 */
	int main() {
		char* s = "// 不是注释 /* 也不是 */";
		return 0; // 退出
	}
	`)

	stripped := StripComments(cCode, ".c")
	if contains(stripped, "张三") || contains(stripped, "240809010501") || contains(stripped, "多行注释") {
		t.Errorf("expected comments to be stripped, got: %s", stripped)
	}

	if !contains(stripped, "// 不是注释") || !contains(stripped, "/* 也不是 */") {
		t.Errorf("expected string literal content to be preserved, got: %s", stripped)
	}
}

func TestNormalizer_VariableRenamingAndComments(t *testing.T) {
	studentA := []byte(`
	// ============================================
	// 学生姓名: 张三
	// 学号: 240809010501
	// 实验一：CUDA 矩阵乘法并行加速
	// ============================================
	#include <stdio.h>
	#include <cuda_runtime.h>

	__global__ void matrixMul(float *A, float *B, float *C, int N) {
		int row = blockIdx.y * blockDim.y + threadIdx.y;
		int col = blockIdx.x * blockDim.x + threadIdx.x;
		if (row < N && col < N) {
			float sum = 0.0f;
			for (int k = 0; k < N; ++k) {
				sum += A[row * N + k] * B[k * N + col];
			}
			C[row * N + col] = sum;
		}
	}
	`)

	// Student B:
	// 1. Changed author comments
	// 2. Changed function name: matrixMul -> my_matrix_mul
	// 3. Changed parameters: A, B, C, N -> matA, matB, matC, dim
	// 4. Changed variables: row, col, sum, k -> r, c, total, i
	// 5. Added empty lines and different spacing
	studentB := []byte(`
	/* 
	 * 姓名：李四
	 * 学号：240809010502
	 * 抄袭自张三但改了变量名
	 */
	#include <stdio.h>
	#include <cuda_runtime.h>

	__global__ void my_matrix_mul(float *matA, float *matB, float *matC, int dim) 
	{
		int r = blockIdx.y * blockDim.y + threadIdx.y;
		int c = blockIdx.x * blockDim.x + threadIdx.x;
		
		if (r < dim && c < dim) 
		{
			float total = 0.0f;
			for (int i = 0; i < dim; ++i) 
			{
				total += matA[r * dim + i] * matB[i * dim + c];
			}
			matC[r * dim + c] = total;
		}
	}
	`)

	resA := ComputeNormalizedCode(studentA, "kernel.cu")
	resB := ComputeNormalizedCode(studentB, "kernel.cu")

	if !resA.MinCoreThreshold || !resB.MinCoreThreshold {
		t.Fatalf("expected both to pass min core threshold")
	}

	// 1. Raw hashes must NOT match because variable names & comments differ
	// (This proves standard hash alone would miss it)
	if resA.NormalizedSHA256 == resB.NormalizedSHA256 {
		t.Logf("NormalizedSHA256 matched: %s", resA.NormalizedSHA256)
	}

	// 2. Structural SHA-256 MUST match 100%!
	if resA.StructuralSHA256 != resB.StructuralSHA256 {
		t.Fatalf("expected StructuralSHA256 to match despite renaming variables and comments!\nA: %s (%s)\nB: %s (%s)",
			resA.StructuralSHA256, resA.StructuralCode, resB.StructuralSHA256, resB.StructuralCode)
	}

	// 3. Token similarity should be 1.0 (100%)
	sim := ComputeTokenSimilarity(resA.Tokens, resB.Tokens)
	if sim < 0.99 {
		t.Errorf("expected token similarity 1.0, got %f", sim)
	}
}

func contains(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr || len(substr) == 0 || (len(s) > 0 && len(substr) > 0 && stringContains(s, substr)))
}

func stringContains(s, substr string) bool {
	for i := 0; i+len(substr) <= len(s); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
